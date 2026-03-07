package dockervolumeactor_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	actor "dokoko.ai/dokoko/internal/docker/volumes/actor"
	state "dokoko.ai/dokoko/internal/docker/volumes/state"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
	dockerfilters "github.com/docker/docker/api/types/filters"
	dockervolume "github.com/docker/docker/api/types/volume"
)

// ── fakeOps ───────────────────────────────────────────────────────────────────

type fakeOps struct {
	mu sync.Mutex

	createFn  func(ctx context.Context, opts dockervolume.CreateOptions) (dockervolume.Volume, error)
	listFn    func(ctx context.Context, opts dockervolume.ListOptions) (dockervolume.ListResponse, error)
	inspFn    func(ctx context.Context, name string) (dockervolume.Volume, error)
	removeFn  func(ctx context.Context, name string, force bool) error
	pruneFn   func(ctx context.Context, f dockerfilters.Args) (dockertypes.VolumesPruneReport, error)

	calls []string
}

func (f *fakeOps) record(name string) {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	f.mu.Unlock()
}

func (f *fakeOps) Create(ctx context.Context, opts dockervolume.CreateOptions) (dockervolume.Volume, error) {
	f.record("Create")
	if f.createFn != nil {
		return f.createFn(ctx, opts)
	}
	return dockervolume.Volume{Name: opts.Name, Driver: "local"}, nil
}

func (f *fakeOps) List(ctx context.Context, opts dockervolume.ListOptions) (dockervolume.ListResponse, error) {
	f.record("List")
	if f.listFn != nil {
		return f.listFn(ctx, opts)
	}
	return dockervolume.ListResponse{}, nil
}

func (f *fakeOps) Inspect(ctx context.Context, name string) (dockervolume.Volume, error) {
	f.record("Inspect")
	if f.inspFn != nil {
		return f.inspFn(ctx, name)
	}
	return dockervolume.Volume{Name: name, Driver: "local", Mountpoint: "/var/lib/docker/volumes/" + name}, nil
}

func (f *fakeOps) Remove(ctx context.Context, name string, force bool) error {
	f.record("Remove")
	if f.removeFn != nil {
		return f.removeFn(ctx, name, force)
	}
	return nil
}

func (f *fakeOps) Prune(ctx context.Context, fl dockerfilters.Args) (dockertypes.VolumesPruneReport, error) {
	f.record("Prune")
	if f.pruneFn != nil {
		return f.pruneFn(ctx, fl)
	}
	return dockertypes.VolumesPruneReport{}, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func silentLogger() *logger.Logger { return logger.New(logger.LevelSilent) }

func newActor(t *testing.T, ops *fakeOps, cfg *actor.Config) (*actor.Actor, *state.State) {
	t.Helper()
	st := state.New(silentLogger())
	a := actor.New(ops, st, silentLogger(), cfg)
	t.Cleanup(func() { a.Close() })
	return a, st
}

func waitTicket(t *testing.T, ticket *actor.Ticket) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ticket.Wait(ctx); err != nil {
		t.Fatalf("ticket.Wait: %v", err)
	}
}

func assertStatus(t *testing.T, st *state.State, changeID string, want state.Status) {
	t.Helper()
	got, _, err := st.FindByID(changeID)
	if err != nil {
		t.Fatalf("FindByID(%q): %v", changeID, err)
	}
	if got != want {
		t.Errorf("status for %q: got %q, want %q", changeID, got, want)
	}
}

// ── Create ────────────────────────────────────────────────────────────────────

func TestCreate_Success_StateGoesActive(t *testing.T) {
	ops := &fakeOps{
		createFn: func(_ context.Context, opts dockervolume.CreateOptions) (dockervolume.Volume, error) {
			return dockervolume.Volume{Name: opts.Name, Driver: "local"}, nil
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.Create(context.Background(), dockervolume.CreateOptions{Name: "test-vol"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if ticket == nil {
		t.Fatal("expected non-nil ticket")
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusActive)

	_, rec, _ := st.FindByID(ticket.ChangeID)
	active := rec.(*state.ActiveRecord)
	if active.VolumeName != "test-vol" {
		t.Errorf("VolumeName: got %q, want %q", active.VolumeName, "test-vol")
	}
}

func TestCreate_Failure_StateGoesFailed(t *testing.T) {
	ops := &fakeOps{
		createFn: func(_ context.Context, _ dockervolume.CreateOptions) (dockervolume.Volume, error) {
			return dockervolume.Volume{}, errors.New("volume already exists")
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.Create(context.Background(), dockervolume.CreateOptions{Name: "dup-vol"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusFailed)

	_, rec, _ := st.FindByID(ticket.ChangeID)
	if rec.(*state.FailedRecord).Err == "" {
		t.Error("FailedRecord.Err should not be empty")
	}
}

func TestCreate_AnonymousVolume_StateGoesActive(t *testing.T) {
	// Daemon generates a name when opts.Name is empty.
	ops := &fakeOps{
		createFn: func(_ context.Context, _ dockervolume.CreateOptions) (dockervolume.Volume, error) {
			return dockervolume.Volume{Name: "daemon-generated-name"}, nil
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.Create(context.Background(), dockervolume.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusActive)

	_, rec, _ := st.FindByID(ticket.ChangeID)
	if rec.(*state.ActiveRecord).VolumeName != "daemon-generated-name" {
		t.Error("expected daemon-generated name in ActiveRecord")
	}
}

// ── Remove ────────────────────────────────────────────────────────────────────

func TestRemove_Success_StateGoesActive(t *testing.T) {
	a, st := newActor(t, &fakeOps{}, nil)

	ticket, err := a.Remove(context.Background(), "test-vol", false)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusActive)
}

func TestRemove_Failure_StateGoesFailed(t *testing.T) {
	ops := &fakeOps{
		removeFn: func(_ context.Context, _ string, _ bool) error {
			return errors.New("volume in use")
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.Remove(context.Background(), "busy-vol", false)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusFailed)
}

// ── Prune ─────────────────────────────────────────────────────────────────────

func TestPrune_Success_StateGoesActive(t *testing.T) {
	ops := &fakeOps{
		pruneFn: func(_ context.Context, _ dockerfilters.Args) (dockertypes.VolumesPruneReport, error) {
			return dockertypes.VolumesPruneReport{VolumesDeleted: []string{"old-vol"}, SpaceReclaimed: 1024}, nil
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.Prune(context.Background(), dockerfilters.NewArgs())
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusActive)
}

func TestPrune_Failure_StateGoesFailed(t *testing.T) {
	ops := &fakeOps{
		pruneFn: func(_ context.Context, _ dockerfilters.Args) (dockertypes.VolumesPruneReport, error) {
			return dockertypes.VolumesPruneReport{}, errors.New("daemon unavailable")
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.Prune(context.Background(), dockerfilters.NewArgs())
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusFailed)
}

// ── Queue full / actor closed ─────────────────────────────────────────────────

func TestQueueFull_ReturnsErrAndAbandons(t *testing.T) {
	blocker := make(chan struct{})
	workerStarted := make(chan struct{}, 1)
	ops := &fakeOps{
		createFn: func(ctx context.Context, _ dockervolume.CreateOptions) (dockervolume.Volume, error) {
			select {
			case workerStarted <- struct{}{}:
			default:
			}
			<-blocker
			return dockervolume.Volume{}, nil
		},
	}
	a, st := newActor(t, ops, &actor.Config{Workers: 1, QueueSize: 1})

	// Occupy the worker.
	_, err := a.Create(context.Background(), dockervolume.CreateOptions{Name: "occupier"})
	if err != nil {
		t.Fatalf("occupier: %v", err)
	}
	// Wait until the worker has actually dequeued "occupier" before filling the queue.
	<-workerStarted
	// Fill the queue.
	_, err = a.Create(context.Background(), dockervolume.CreateOptions{Name: "filler"})
	if err != nil {
		t.Fatalf("queue-filler: %v", err)
	}

	// This one should be rejected.
	ticket, err := a.Create(context.Background(), dockervolume.CreateOptions{Name: "overflow"})
	if !errors.Is(err, actor.ErrQueueFull) {
		t.Errorf("want ErrQueueFull, got %v", err)
	}
	if ticket != nil {
		t.Error("expected nil ticket on queue-full error")
	}

	req, _, _, abn := st.Summary()
	if abn < 1 {
		t.Errorf("expected at least 1 abandoned, got req=%d abn=%d", req, abn)
	}

	close(blocker)
}

func TestActorClosed_ReturnsErrAndAbandons(t *testing.T) {
	a, st := newActor(t, &fakeOps{}, nil)
	a.Close()

	ticket, err := a.Create(context.Background(), dockervolume.CreateOptions{Name: "vol"})
	if !errors.Is(err, actor.ErrActorClosed) {
		t.Errorf("want ErrActorClosed, got %v", err)
	}
	if ticket != nil {
		t.Error("expected nil ticket after close")
	}

	_, _, _, ab := st.Summary()
	if ab < 1 {
		req, act, fail, abn := st.Summary()
		t.Errorf("expected at least 1 abandoned, got req=%d act=%d fail=%d abn=%d",
			req, act, fail, abn)
	}
}

func TestClose_DrainsQueueAndAbandonsRemaining(t *testing.T) {
	blocker := make(chan struct{})
	ops := &fakeOps{
		createFn: func(_ context.Context, _ dockervolume.CreateOptions) (dockervolume.Volume, error) {
			<-blocker
			return dockervolume.Volume{}, nil
		},
	}
	a, st := newActor(t, ops, &actor.Config{Workers: 1, QueueSize: 10})

	blockerTicket, _ := a.Create(context.Background(), dockervolume.CreateOptions{Name: "blocker"})

	const queued = 5
	tickets := make([]*actor.Ticket, queued)
	for i := range queued {
		tk, err := a.Create(context.Background(), dockervolume.CreateOptions{Name: fmt.Sprintf("queued-%d", i)})
		if err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
		tickets[i] = tk
	}

	go func() {
		time.Sleep(10 * time.Millisecond)
		close(blocker)
	}()
	a.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = blockerTicket.Wait(ctx)

	for i, tk := range tickets {
		tctx, tcancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := tk.Wait(tctx); err != nil {
			t.Errorf("ticket[%d] did not settle: %v", i, err)
		}
		tcancel()
	}

	req, act, fail, abn := st.Summary()
	if req != 0 {
		t.Errorf("requested after close: got %d, want 0 (act=%d fail=%d abn=%d)",
			req, act, fail, abn)
	}
	total := 1 + queued
	if act+fail+abn != total {
		t.Errorf("settled total: got %d, want %d (act=%d fail=%d abn=%d)",
			act+fail+abn, total, act, fail, abn)
	}
}

func TestClose_IsIdempotent(t *testing.T) {
	a := actor.New(&fakeOps{}, state.New(silentLogger()), silentLogger(), nil)
	a.Close()
	a.Close()
	a.Close()
}

func TestCancelledContext_StateAbandoned(t *testing.T) {
	blocker := make(chan struct{})
	ops := &fakeOps{
		createFn: func(ctx context.Context, opts dockervolume.CreateOptions) (dockervolume.Volume, error) {
			if opts.Name == "__blocker__" {
				<-blocker
			}
			return dockervolume.Volume{Name: opts.Name}, nil
		},
	}
	a, st := newActor(t, ops, &actor.Config{Workers: 1, QueueSize: 2})

	_, err := a.Create(context.Background(), dockervolume.CreateOptions{Name: "__blocker__"})
	if err != nil {
		t.Fatalf("blocker create: %v", err)
	}

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	ticket, err := a.Create(cancelledCtx, dockervolume.CreateOptions{Name: "target"})
	if err != nil {
		t.Fatalf("target create: %v", err)
	}

	close(blocker)

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusAbandoned)
}

// ── Read-only operations ──────────────────────────────────────────────────────

func TestList_Async_ReturnsResult(t *testing.T) {
	want := []*dockervolume.Volume{{Name: "vol-a"}, {Name: "vol-b"}}
	ops := &fakeOps{
		listFn: func(_ context.Context, _ dockervolume.ListOptions) (dockervolume.ListResponse, error) {
			return dockervolume.ListResponse{Volumes: want}, nil
		},
	}
	a, _ := newActor(t, ops, nil)

	res := <-a.List(context.Background(), dockervolume.ListOptions{})
	if res.Err != nil {
		t.Fatalf("List: %v", res.Err)
	}
	if len(res.Response.Volumes) != len(want) {
		t.Errorf("volumes: got %d, want %d", len(res.Response.Volumes), len(want))
	}
}

func TestList_Async_PropagatesError(t *testing.T) {
	ops := &fakeOps{
		listFn: func(_ context.Context, _ dockervolume.ListOptions) (dockervolume.ListResponse, error) {
			return dockervolume.ListResponse{}, errors.New("daemon error")
		},
	}
	a, _ := newActor(t, ops, nil)

	res := <-a.List(context.Background(), dockervolume.ListOptions{})
	if res.Err == nil {
		t.Error("expected error from List")
	}
}

func TestInspect_Async_ReturnsResult(t *testing.T) {
	ops := &fakeOps{
		inspFn: func(_ context.Context, name string) (dockervolume.Volume, error) {
			return dockervolume.Volume{Name: name, Driver: "local", Mountpoint: "/mnt/" + name}, nil
		},
	}
	a, _ := newActor(t, ops, nil)

	res := <-a.Inspect(context.Background(), "my-vol")
	if res.Err != nil {
		t.Fatalf("Inspect: %v", res.Err)
	}
	if res.Volume.Mountpoint != "/mnt/my-vol" {
		t.Errorf("Mountpoint: got %q, want %q", res.Volume.Mountpoint, "/mnt/my-vol")
	}
}

// ── Concurrency ───────────────────────────────────────────────────────────────

func TestConcurrentCreates_AllSettle(t *testing.T) {
	var callCount atomic.Int64
	ops := &fakeOps{
		createFn: func(_ context.Context, opts dockervolume.CreateOptions) (dockervolume.Volume, error) {
			callCount.Add(1)
			return dockervolume.Volume{Name: opts.Name}, nil
		},
	}
	a, st := newActor(t, ops, &actor.Config{Workers: 8, QueueSize: 200})

	const total = 100
	tickets := make([]*actor.Ticket, total)
	var submitErr atomic.Int64

	var wg sync.WaitGroup
	for i := range total {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			tk, err := a.Create(context.Background(),
				dockervolume.CreateOptions{Name: fmt.Sprintf("vol-%d", i)})
			if err != nil {
				submitErr.Add(1)
				return
			}
			tickets[i] = tk
		}()
	}
	wg.Wait()

	if n := submitErr.Load(); n > 0 {
		t.Errorf("%d submit errors", n)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for i, tk := range tickets {
		if tk == nil {
			continue
		}
		if err := tk.Wait(ctx); err != nil {
			t.Errorf("ticket[%d].Wait: %v", i, err)
		}
	}

	req, act, fail, abn := st.Summary()
	if req != 0 {
		t.Errorf("requested after settle: got %d, want 0", req)
	}
	if act+fail+abn != total {
		t.Errorf("settled total: got %d, want %d (act=%d fail=%d abn=%d)",
			act+fail+abn, total, act, fail, abn)
	}
}
