package dockernetworkactor_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	actor "dokoko.ai/dokoko/internal/docker/networks/actor"
	state "dokoko.ai/dokoko/internal/docker/networks/state"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
	dockerfilters "github.com/docker/docker/api/types/filters"
)

// ── fakeOps ───────────────────────────────────────────────────────────────────

type fakeOps struct {
	mu sync.Mutex

	createFn  func(ctx context.Context, name string, opts dockertypes.NetworkCreate) (dockertypes.NetworkCreateResponse, error)
	listFn    func(ctx context.Context, opts dockertypes.NetworkListOptions) ([]dockertypes.NetworkResource, error)
	inspFn    func(ctx context.Context, networkID string, opts dockertypes.NetworkInspectOptions) (dockertypes.NetworkResource, error)
	removeFn  func(ctx context.Context, networkID string) error
	pruneFn   func(ctx context.Context, f dockerfilters.Args) (dockertypes.NetworksPruneReport, error)

	calls []string
}

func (f *fakeOps) record(name string) {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	f.mu.Unlock()
}

func (f *fakeOps) Create(ctx context.Context, name string, opts dockertypes.NetworkCreate) (dockertypes.NetworkCreateResponse, error) {
	f.record("Create")
	if f.createFn != nil {
		return f.createFn(ctx, name, opts)
	}
	return dockertypes.NetworkCreateResponse{ID: "fake-id-" + name}, nil
}

func (f *fakeOps) List(ctx context.Context, opts dockertypes.NetworkListOptions) ([]dockertypes.NetworkResource, error) {
	f.record("List")
	if f.listFn != nil {
		return f.listFn(ctx, opts)
	}
	return nil, nil
}

func (f *fakeOps) Inspect(ctx context.Context, networkID string, opts dockertypes.NetworkInspectOptions) (dockertypes.NetworkResource, error) {
	f.record("Inspect")
	if f.inspFn != nil {
		return f.inspFn(ctx, networkID, opts)
	}
	return dockertypes.NetworkResource{ID: networkID, Name: networkID, Driver: "bridge"}, nil
}

func (f *fakeOps) Remove(ctx context.Context, networkID string) error {
	f.record("Remove")
	if f.removeFn != nil {
		return f.removeFn(ctx, networkID)
	}
	return nil
}

func (f *fakeOps) Prune(ctx context.Context, fl dockerfilters.Args) (dockertypes.NetworksPruneReport, error) {
	f.record("Prune")
	if f.pruneFn != nil {
		return f.pruneFn(ctx, fl)
	}
	return dockertypes.NetworksPruneReport{}, nil
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
		createFn: func(_ context.Context, name string, _ dockertypes.NetworkCreate) (dockertypes.NetworkCreateResponse, error) {
			return dockertypes.NetworkCreateResponse{ID: "net-id-" + name}, nil
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.Create(context.Background(), "test-net", dockertypes.NetworkCreate{Driver: "bridge"})
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
	if active.NetworkID != "net-id-test-net" {
		t.Errorf("NetworkID: got %q, want %q", active.NetworkID, "net-id-test-net")
	}
}

func TestCreate_Failure_StateGoesFailed(t *testing.T) {
	ops := &fakeOps{
		createFn: func(_ context.Context, _ string, _ dockertypes.NetworkCreate) (dockertypes.NetworkCreateResponse, error) {
			return dockertypes.NetworkCreateResponse{}, errors.New("network already exists")
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.Create(context.Background(), "dup-net", dockertypes.NetworkCreate{})
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

// ── Remove ────────────────────────────────────────────────────────────────────

func TestRemove_Success_StateGoesActive(t *testing.T) {
	a, st := newActor(t, &fakeOps{}, nil)

	ticket, err := a.Remove(context.Background(), "test-net-id")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusActive)
}

func TestRemove_Failure_StateGoesFailed(t *testing.T) {
	ops := &fakeOps{
		removeFn: func(_ context.Context, _ string) error {
			return errors.New("network has active endpoints")
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.Remove(context.Background(), "busy-net-id")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusFailed)
}

// ── Prune ─────────────────────────────────────────────────────────────────────

func TestPrune_Success_StateGoesActive(t *testing.T) {
	ops := &fakeOps{
		pruneFn: func(_ context.Context, _ dockerfilters.Args) (dockertypes.NetworksPruneReport, error) {
			return dockertypes.NetworksPruneReport{NetworksDeleted: []string{"old-net"}}, nil
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
		pruneFn: func(_ context.Context, _ dockerfilters.Args) (dockertypes.NetworksPruneReport, error) {
			return dockertypes.NetworksPruneReport{}, errors.New("daemon unavailable")
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
	workerBusy := make(chan struct{}, 1) // signals that the worker has started

	ops := &fakeOps{
		createFn: func(ctx context.Context, _ string, _ dockertypes.NetworkCreate) (dockertypes.NetworkCreateResponse, error) {
			select {
			case workerBusy <- struct{}{}:
			default:
			}
			<-blocker
			return dockertypes.NetworkCreateResponse{}, nil
		},
	}
	a, st := newActor(t, ops, &actor.Config{Workers: 1, QueueSize: 1})

	// Ensure blocker is closed even if the test fails early (prevents cleanup hang).
	var closeOnce sync.Once
	t.Cleanup(func() { closeOnce.Do(func() { close(blocker) }) })

	// Occupy the worker.
	_, err := a.Create(context.Background(), "occupier", dockertypes.NetworkCreate{})
	if err != nil {
		t.Fatalf("occupier: %v", err)
	}

	// Wait for the worker to actually start executing occupier, so the queue is empty.
	select {
	case <-workerBusy:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for worker to start occupier")
	}

	// Fill the queue.
	_, err = a.Create(context.Background(), "filler", dockertypes.NetworkCreate{})
	if err != nil {
		t.Fatalf("queue-filler: %v", err)
	}

	// This one should be rejected.
	ticket, err := a.Create(context.Background(), "overflow", dockertypes.NetworkCreate{})
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

	closeOnce.Do(func() { close(blocker) })
}

func TestActorClosed_ReturnsErrAndAbandons(t *testing.T) {
	a, st := newActor(t, &fakeOps{}, nil)
	a.Close()

	ticket, err := a.Create(context.Background(), "net", dockertypes.NetworkCreate{})
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
		createFn: func(_ context.Context, _ string, _ dockertypes.NetworkCreate) (dockertypes.NetworkCreateResponse, error) {
			<-blocker
			return dockertypes.NetworkCreateResponse{}, nil
		},
	}
	a, st := newActor(t, ops, &actor.Config{Workers: 1, QueueSize: 10})

	blockerTicket, _ := a.Create(context.Background(), "blocker", dockertypes.NetworkCreate{})

	const queued = 5
	tickets := make([]*actor.Ticket, queued)
	for i := range queued {
		tk, err := a.Create(context.Background(), fmt.Sprintf("queued-%d", i), dockertypes.NetworkCreate{})
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
		createFn: func(ctx context.Context, name string, _ dockertypes.NetworkCreate) (dockertypes.NetworkCreateResponse, error) {
			if name == "__blocker__" {
				<-blocker
			}
			return dockertypes.NetworkCreateResponse{ID: "id-" + name}, nil
		},
	}
	a, st := newActor(t, ops, &actor.Config{Workers: 1, QueueSize: 2})

	_, err := a.Create(context.Background(), "__blocker__", dockertypes.NetworkCreate{})
	if err != nil {
		t.Fatalf("blocker create: %v", err)
	}

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	ticket, err := a.Create(cancelledCtx, "target", dockertypes.NetworkCreate{})
	if err != nil {
		t.Fatalf("target create: %v", err)
	}

	close(blocker)

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusAbandoned)
}

// ── Read-only operations ──────────────────────────────────────────────────────

func TestList_Async_ReturnsResult(t *testing.T) {
	want := []dockertypes.NetworkResource{{ID: "id-a", Name: "net-a"}, {ID: "id-b", Name: "net-b"}}
	ops := &fakeOps{
		listFn: func(_ context.Context, _ dockertypes.NetworkListOptions) ([]dockertypes.NetworkResource, error) {
			return want, nil
		},
	}
	a, _ := newActor(t, ops, nil)

	res := <-a.List(context.Background(), dockertypes.NetworkListOptions{})
	if res.Err != nil {
		t.Fatalf("List: %v", res.Err)
	}
	if len(res.Networks) != len(want) {
		t.Errorf("networks: got %d, want %d", len(res.Networks), len(want))
	}
}

func TestList_Async_PropagatesError(t *testing.T) {
	ops := &fakeOps{
		listFn: func(_ context.Context, _ dockertypes.NetworkListOptions) ([]dockertypes.NetworkResource, error) {
			return nil, errors.New("daemon error")
		},
	}
	a, _ := newActor(t, ops, nil)

	res := <-a.List(context.Background(), dockertypes.NetworkListOptions{})
	if res.Err == nil {
		t.Error("expected error from List")
	}
}

func TestInspect_Async_ReturnsResult(t *testing.T) {
	ops := &fakeOps{
		inspFn: func(_ context.Context, networkID string, _ dockertypes.NetworkInspectOptions) (dockertypes.NetworkResource, error) {
			return dockertypes.NetworkResource{ID: networkID, Name: "my-net", Driver: "bridge"}, nil
		},
	}
	a, _ := newActor(t, ops, nil)

	res := <-a.Inspect(context.Background(), "abc123", dockertypes.NetworkInspectOptions{})
	if res.Err != nil {
		t.Fatalf("Inspect: %v", res.Err)
	}
	if res.Network.Name != "my-net" {
		t.Errorf("Name: got %q, want %q", res.Network.Name, "my-net")
	}
}

// ── Concurrency ───────────────────────────────────────────────────────────────

func TestConcurrentCreates_AllSettle(t *testing.T) {
	var callCount atomic.Int64
	ops := &fakeOps{
		createFn: func(_ context.Context, name string, _ dockertypes.NetworkCreate) (dockertypes.NetworkCreateResponse, error) {
			callCount.Add(1)
			return dockertypes.NetworkCreateResponse{ID: "id-" + name}, nil
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
				fmt.Sprintf("net-%d", i), dockertypes.NetworkCreate{})
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
