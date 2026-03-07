package dockernetworkclerk_test

import (
	"context"
	"errors"
	"testing"

	dockernetworkclerk "dokoko.ai/dokoko/internal/docker/networks/clerk"
	dockernetworkactor "dokoko.ai/dokoko/internal/docker/networks/actor"
	dockernetworkstate "dokoko.ai/dokoko/internal/docker/networks/state"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
	dockerfilters "github.com/docker/docker/api/types/filters"
)

// ── fakeActor ────────────────────────────────────────────────────────────────

type fakeActor struct {
	createFn  func(ctx context.Context, name string, opts dockertypes.NetworkCreate) (*dockernetworkactor.Ticket, error)
	removeFn  func(ctx context.Context, networkID string) (*dockernetworkactor.Ticket, error)
	pruneFn   func(ctx context.Context, f dockerfilters.Args) (*dockernetworkactor.Ticket, error)
	listFn    func(ctx context.Context, opts dockertypes.NetworkListOptions) <-chan dockernetworkactor.ListResult
	inspectFn func(ctx context.Context, networkID string, opts dockertypes.NetworkInspectOptions) <-chan dockernetworkactor.InspectResult
}

func closedTicket(changeID string) *dockernetworkactor.Ticket {
	done := make(chan struct{})
	close(done)
	return &dockernetworkactor.Ticket{ChangeID: changeID, Done: done}
}

func (f *fakeActor) Create(ctx context.Context, name string, opts dockertypes.NetworkCreate) (*dockernetworkactor.Ticket, error) {
	if f.createFn != nil {
		return f.createFn(ctx, name, opts)
	}
	return closedTicket("nchg-create"), nil
}

func (f *fakeActor) Remove(ctx context.Context, networkID string) (*dockernetworkactor.Ticket, error) {
	if f.removeFn != nil {
		return f.removeFn(ctx, networkID)
	}
	return closedTicket("nchg-remove"), nil
}

func (f *fakeActor) Prune(ctx context.Context, fl dockerfilters.Args) (*dockernetworkactor.Ticket, error) {
	if f.pruneFn != nil {
		return f.pruneFn(ctx, fl)
	}
	return closedTicket("nchg-prune"), nil
}

func (f *fakeActor) List(ctx context.Context, opts dockertypes.NetworkListOptions) <-chan dockernetworkactor.ListResult {
	if f.listFn != nil {
		return f.listFn(ctx, opts)
	}
	ch := make(chan dockernetworkactor.ListResult, 1)
	ch <- dockernetworkactor.ListResult{}
	return ch
}

func (f *fakeActor) Inspect(ctx context.Context, networkID string, opts dockertypes.NetworkInspectOptions) <-chan dockernetworkactor.InspectResult {
	if f.inspectFn != nil {
		return f.inspectFn(ctx, networkID, opts)
	}
	ch := make(chan dockernetworkactor.InspectResult, 1)
	ch <- dockernetworkactor.InspectResult{}
	return ch
}

// ── helpers ───────────────────────────────────────────────────────────────────

func silentLogger() *logger.Logger { return logger.New(logger.LevelSilent) }

func newTestClerk(act *fakeActor) (*dockernetworkclerk.Clerk, *dockernetworkstate.State, *dockernetworkstate.NetworkStore) {
	st := dockernetworkstate.New(silentLogger())
	store := dockernetworkstate.NewNetworkStore(silentLogger())
	cl := dockernetworkclerk.New(act, st, store, silentLogger())
	return cl, st, store
}

// ── Refresh ───────────────────────────────────────────────────────────────────

func TestRefresh_PopulatesStore(t *testing.T) {
	act := &fakeActor{
		listFn: func(_ context.Context, _ dockertypes.NetworkListOptions) <-chan dockernetworkactor.ListResult {
			ch := make(chan dockernetworkactor.ListResult, 1)
			ch <- dockernetworkactor.ListResult{
				Networks: []dockertypes.NetworkResource{
					{ID: "aabbcc112233", Name: "bridge", Driver: "bridge", Scope: "local"},
					{ID: "ddeeff445566", Name: "my-overlay", Driver: "overlay", Scope: "swarm"},
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

	rec, ok := store.Get("aabbcc112233")
	if !ok {
		t.Fatal("expected aabbcc112233 in store")
	}
	if rec.Status != dockernetworkstate.NetworkStatusPresent {
		t.Errorf("status: got %q, want present", rec.Status)
	}
	if rec.Name != "bridge" {
		t.Errorf("Name: got %q, want bridge", rec.Name)
	}
	if rec.Driver != "bridge" {
		t.Errorf("Driver: got %q, want bridge", rec.Driver)
	}
}

func TestRefresh_ReconcilesMissingNetworks(t *testing.T) {
	call := 0
	act := &fakeActor{
		listFn: func(_ context.Context, _ dockertypes.NetworkListOptions) <-chan dockernetworkactor.ListResult {
			ch := make(chan dockernetworkactor.ListResult, 1)
			if call == 0 {
				ch <- dockernetworkactor.ListResult{
					Networks: []dockertypes.NetworkResource{
						{ID: "net-aaa", Name: "net-a", Driver: "bridge"},
						{ID: "net-bbb", Name: "net-b", Driver: "bridge"},
					},
				}
			} else {
				ch <- dockernetworkactor.ListResult{
					Networks: []dockertypes.NetworkResource{
						{ID: "net-bbb", Name: "net-b", Driver: "bridge"},
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

	rec, ok := store.Get("net-aaa")
	if !ok {
		t.Fatal("expected net-aaa in store")
	}
	if rec.Status != dockernetworkstate.NetworkStatusDeletedOutOfBand {
		t.Errorf("net-a status: got %q, want deleted_out_of_band", rec.Status)
	}
}

func TestRefresh_PropagatesListError(t *testing.T) {
	act := &fakeActor{
		listFn: func(_ context.Context, _ dockertypes.NetworkListOptions) <-chan dockernetworkactor.ListResult {
			ch := make(chan dockernetworkactor.ListResult, 1)
			ch <- dockernetworkactor.ListResult{Err: errors.New("daemon error")}
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
		createFn: func(_ context.Context, name string, _ dockertypes.NetworkCreate) (*dockernetworkactor.Ticket, error) {
			gotName = name
			return closedTicket("nchg-1"), nil
		},
	}
	cl, _, _ := newTestClerk(act)

	ticket, err := cl.Create(context.Background(), "my-net", dockertypes.NetworkCreate{Driver: "bridge"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if ticket == nil {
		t.Fatal("expected non-nil ticket")
	}
	if gotName != "my-net" {
		t.Errorf("Create called with name %q, want my-net", gotName)
	}
}

func TestCreate_PropagatesError(t *testing.T) {
	act := &fakeActor{
		createFn: func(_ context.Context, _ string, _ dockertypes.NetworkCreate) (*dockernetworkactor.Ticket, error) {
			return nil, errors.New("queue full")
		},
	}
	cl, _, _ := newTestClerk(act)

	_, err := cl.Create(context.Background(), "net", dockertypes.NetworkCreate{})
	if err == nil {
		t.Error("expected error propagated from actor")
	}
}

func TestRemove_DelegatesToActor(t *testing.T) {
	var gotID string
	act := &fakeActor{
		removeFn: func(_ context.Context, networkID string) (*dockernetworkactor.Ticket, error) {
			gotID = networkID
			return closedTicket("nchg-rm"), nil
		},
	}
	cl, _, _ := newTestClerk(act)

	_, err := cl.Remove(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if gotID != "abc123" {
		t.Errorf("Remove called with %q, want abc123", gotID)
	}
}

func TestPrune_DelegatesToActor(t *testing.T) {
	called := false
	act := &fakeActor{
		pruneFn: func(_ context.Context, _ dockerfilters.Args) (*dockernetworkactor.Ticket, error) {
			called = true
			return closedTicket("nchg-prune"), nil
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
