package dockervolumeclerk_test

import (
	"context"
	"errors"
	"testing"

	dockervolumeclerk "dokoko.ai/dokoko/internal/docker/volumes/clerk"
	dockervolumeactor "dokoko.ai/dokoko/internal/docker/volumes/actor"
	dockervolumestate "dokoko.ai/dokoko/internal/docker/volumes/state"
	"dokoko.ai/dokoko/pkg/logger"
	dockerfilters "github.com/docker/docker/api/types/filters"
	dockervolume "github.com/docker/docker/api/types/volume"
)

// ── fakeActor ────────────────────────────────────────────────────────────────

type fakeActor struct {
	createFn  func(ctx context.Context, opts dockervolume.CreateOptions) (*dockervolumeactor.Ticket, error)
	removeFn  func(ctx context.Context, name string, force bool) (*dockervolumeactor.Ticket, error)
	pruneFn   func(ctx context.Context, f dockerfilters.Args) (*dockervolumeactor.Ticket, error)
	listFn    func(ctx context.Context, opts dockervolume.ListOptions) <-chan dockervolumeactor.ListResult
	inspectFn func(ctx context.Context, name string) <-chan dockervolumeactor.InspectResult
}

func closedTicket(changeID string) *dockervolumeactor.Ticket {
	done := make(chan struct{})
	close(done)
	return &dockervolumeactor.Ticket{ChangeID: changeID, Done: done}
}

func (f *fakeActor) Create(ctx context.Context, opts dockervolume.CreateOptions) (*dockervolumeactor.Ticket, error) {
	if f.createFn != nil {
		return f.createFn(ctx, opts)
	}
	return closedTicket("vchg-create"), nil
}

func (f *fakeActor) Remove(ctx context.Context, name string, force bool) (*dockervolumeactor.Ticket, error) {
	if f.removeFn != nil {
		return f.removeFn(ctx, name, force)
	}
	return closedTicket("vchg-remove"), nil
}

func (f *fakeActor) Prune(ctx context.Context, fl dockerfilters.Args) (*dockervolumeactor.Ticket, error) {
	if f.pruneFn != nil {
		return f.pruneFn(ctx, fl)
	}
	return closedTicket("vchg-prune"), nil
}

func (f *fakeActor) List(ctx context.Context, opts dockervolume.ListOptions) <-chan dockervolumeactor.ListResult {
	if f.listFn != nil {
		return f.listFn(ctx, opts)
	}
	ch := make(chan dockervolumeactor.ListResult, 1)
	ch <- dockervolumeactor.ListResult{}
	return ch
}

func (f *fakeActor) Inspect(ctx context.Context, name string) <-chan dockervolumeactor.InspectResult {
	if f.inspectFn != nil {
		return f.inspectFn(ctx, name)
	}
	ch := make(chan dockervolumeactor.InspectResult, 1)
	ch <- dockervolumeactor.InspectResult{}
	return ch
}

// ── helpers ───────────────────────────────────────────────────────────────────

func silentLogger() *logger.Logger { return logger.New(logger.LevelSilent) }

func newTestClerk(act *fakeActor) (*dockervolumeclerk.Clerk, *dockervolumestate.State, *dockervolumestate.VolumeStore) {
	st := dockervolumestate.New(silentLogger())
	store := dockervolumestate.NewVolumeStore(silentLogger())
	cl := dockervolumeclerk.New(act, st, store, silentLogger())
	return cl, st, store
}

// ── Refresh ───────────────────────────────────────────────────────────────────

func TestRefresh_PopulatesStore(t *testing.T) {
	act := &fakeActor{
		listFn: func(_ context.Context, _ dockervolume.ListOptions) <-chan dockervolumeactor.ListResult {
			ch := make(chan dockervolumeactor.ListResult, 1)
			ch <- dockervolumeactor.ListResult{
				Response: dockervolume.ListResponse{
					Volumes: []*dockervolume.Volume{
						{Name: "vol-a", Driver: "local", Mountpoint: "/var/lib/docker/volumes/vol-a/_data"},
						{Name: "vol-b", Driver: "local", Mountpoint: "/var/lib/docker/volumes/vol-b/_data"},
					},
				},
			}
			return ch
		},
	}
	cl, _, store := newTestClerk(act)

	if err := cl.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if store.Size() != 2 {
		t.Errorf("store size: got %d, want 2", store.Size())
	}

	rec, ok := store.Get("vol-a")
	if !ok {
		t.Fatal("expected vol-a in store")
	}
	if rec.Status != dockervolumestate.VolumeStatusPresent {
		t.Errorf("vol-a status: got %q, want present", rec.Status)
	}
	if rec.Driver != "local" {
		t.Errorf("vol-a driver: got %q, want local", rec.Driver)
	}
}

func TestRefresh_ReconcilesMissingVolumes(t *testing.T) {
	call := 0
	act := &fakeActor{
		listFn: func(_ context.Context, _ dockervolume.ListOptions) <-chan dockervolumeactor.ListResult {
			ch := make(chan dockervolumeactor.ListResult, 1)
			if call == 0 {
				ch <- dockervolumeactor.ListResult{
					Response: dockervolume.ListResponse{
						Volumes: []*dockervolume.Volume{
							{Name: "vol-a", Driver: "local"},
							{Name: "vol-b", Driver: "local"},
						},
					},
				}
			} else {
				ch <- dockervolumeactor.ListResult{
					Response: dockervolume.ListResponse{
						Volumes: []*dockervolume.Volume{
							{Name: "vol-b", Driver: "local"},
						},
					},
				}
			}
			call++
			return ch
		},
	}
	cl, _, store := newTestClerk(act)

	if err := cl.Refresh(context.Background()); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if err := cl.Refresh(context.Background()); err != nil {
		t.Fatalf("second refresh: %v", err)
	}

	rec, ok := store.Get("vol-a")
	if !ok {
		t.Fatal("expected vol-a in store")
	}
	if rec.Status != dockervolumestate.VolumeStatusDeletedOutOfBand {
		t.Errorf("vol-a status after removal: got %q, want deleted_out_of_band", rec.Status)
	}
}

func TestRefresh_PropagatesListError(t *testing.T) {
	act := &fakeActor{
		listFn: func(_ context.Context, _ dockervolume.ListOptions) <-chan dockervolumeactor.ListResult {
			ch := make(chan dockervolumeactor.ListResult, 1)
			ch <- dockervolumeactor.ListResult{Err: errors.New("daemon error")}
			return ch
		},
	}
	cl, _, _ := newTestClerk(act)

	if err := cl.Refresh(context.Background()); err == nil {
		t.Error("expected error from Refresh when list fails")
	}
}

// ── Mutation delegation ───────────────────────────────────────────────────────

func TestCreate_DelegatesToActor(t *testing.T) {
	var gotName string
	act := &fakeActor{
		createFn: func(_ context.Context, opts dockervolume.CreateOptions) (*dockervolumeactor.Ticket, error) {
			gotName = opts.Name
			return closedTicket("vchg-1"), nil
		},
	}
	cl, _, _ := newTestClerk(act)

	ticket, err := cl.Create(context.Background(), dockervolume.CreateOptions{Name: "my-vol"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if ticket == nil {
		t.Fatal("expected non-nil ticket")
	}
	if gotName != "my-vol" {
		t.Errorf("Create called with name %q, want my-vol", gotName)
	}
}

func TestCreate_PropagatesError(t *testing.T) {
	act := &fakeActor{
		createFn: func(_ context.Context, _ dockervolume.CreateOptions) (*dockervolumeactor.Ticket, error) {
			return nil, errors.New("queue full")
		},
	}
	cl, _, _ := newTestClerk(act)

	_, err := cl.Create(context.Background(), dockervolume.CreateOptions{Name: "x"})
	if err == nil {
		t.Error("expected error propagated from actor")
	}
}

func TestRemove_DelegatesToActor(t *testing.T) {
	var gotName string
	var gotForce bool
	act := &fakeActor{
		removeFn: func(_ context.Context, name string, force bool) (*dockervolumeactor.Ticket, error) {
			gotName = name
			gotForce = force
			return closedTicket("vchg-rm"), nil
		},
	}
	cl, _, _ := newTestClerk(act)

	_, err := cl.Remove(context.Background(), "old-vol", true)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if gotName != "old-vol" || !gotForce {
		t.Errorf("Remove args: name=%q force=%v", gotName, gotForce)
	}
}

func TestPrune_DelegatesToActor(t *testing.T) {
	called := false
	act := &fakeActor{
		pruneFn: func(_ context.Context, _ dockerfilters.Args) (*dockervolumeactor.Ticket, error) {
			called = true
			return closedTicket("vchg-prune"), nil
		},
	}
	cl, _, _ := newTestClerk(act)

	_, err := cl.Prune(context.Background(), dockerfilters.NewArgs())
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if !called {
		t.Error("expected actor Prune to be called")
	}
}

// ── Accessors ─────────────────────────────────────────────────────────────────

func TestState_ReturnsSameInstance(t *testing.T) {
	cl, st, _ := newTestClerk(&fakeActor{})
	if cl.State() != st {
		t.Error("State() returned wrong instance")
	}
}

func TestStore_ReturnsSameInstance(t *testing.T) {
	cl, _, store := newTestClerk(&fakeActor{})
	if cl.Store() != store {
		t.Error("Store() returned wrong instance")
	}
}
