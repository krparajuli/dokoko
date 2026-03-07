package dockerimageactor_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	actor "dokoko.ai/dokoko/internal/docker/images/actor"
	state "dokoko.ai/dokoko/internal/docker/images/state"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
	dockerimage "github.com/docker/docker/api/types/image"
)

// ── fakeOps ───────────────────────────────────────────────────────────────────

// fakeOps satisfies the unexported opsProvider interface via field functions.
// Each field defaults to a no-error implementation so tests only set what they
// care about.
type fakeOps struct {
	mu sync.Mutex

	pullFn   func(ctx context.Context, ref string, opts dockerimage.PullOptions) error
	listFn   func(ctx context.Context, opts dockerimage.ListOptions) ([]dockerimage.Summary, error)
	inspFn   func(ctx context.Context, imageID string) (dockertypes.ImageInspect, error)
	removeFn func(ctx context.Context, imageID string, opts dockerimage.RemoveOptions) ([]dockerimage.DeleteResponse, error)
	tagFn    func(ctx context.Context, source, target string) error
	existsFn func(ctx context.Context, ref string) (bool, error)

	calls []string // names of methods invoked, in order
}

func (f *fakeOps) record(name string) {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	f.mu.Unlock()
}

func (f *fakeOps) Pull(ctx context.Context, ref string, opts dockerimage.PullOptions) error {
	f.record("Pull")
	if f.pullFn != nil {
		return f.pullFn(ctx, ref, opts)
	}
	return nil
}

func (f *fakeOps) List(ctx context.Context, opts dockerimage.ListOptions) ([]dockerimage.Summary, error) {
	f.record("List")
	if f.listFn != nil {
		return f.listFn(ctx, opts)
	}
	return nil, nil
}

func (f *fakeOps) Inspect(ctx context.Context, imageID string) (dockertypes.ImageInspect, error) {
	f.record("Inspect")
	if f.inspFn != nil {
		return f.inspFn(ctx, imageID)
	}
	return dockertypes.ImageInspect{ID: "sha256:fake"}, nil
}

func (f *fakeOps) Remove(ctx context.Context, imageID string, opts dockerimage.RemoveOptions) ([]dockerimage.DeleteResponse, error) {
	f.record("Remove")
	if f.removeFn != nil {
		return f.removeFn(ctx, imageID, opts)
	}
	return nil, nil
}

func (f *fakeOps) Tag(ctx context.Context, source, target string) error {
	f.record("Tag")
	if f.tagFn != nil {
		return f.tagFn(ctx, source, target)
	}
	return nil
}

func (f *fakeOps) Exists(ctx context.Context, ref string) (bool, error) {
	f.record("Exists")
	if f.existsFn != nil {
		return f.existsFn(ctx, ref)
	}
	return true, nil
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

// waitTicket waits for t's Done channel with a test-scoped timeout.
func waitTicket(t *testing.T, ticket *actor.Ticket) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ticket.Wait(ctx); err != nil {
		t.Fatalf("ticket.Wait: %v", err)
	}
}

// assertStatus fetches change status from state and fails if it doesn't match.
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

// ── Pull ──────────────────────────────────────────────────────────────────────

func TestPull_Success_StateGoesActive(t *testing.T) {
	ops := &fakeOps{
		inspFn: func(_ context.Context, _ string) (dockertypes.ImageInspect, error) {
			return dockertypes.ImageInspect{ID: "sha256:busybox"}, nil
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.Pull(context.Background(), "busybox:latest", dockerimage.PullOptions{})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if ticket == nil {
		t.Fatal("expected non-nil ticket")
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusActive)

	// DockerID should be set from the inspect call.
	_, rec, _ := st.FindByID(ticket.ChangeID)
	active := rec.(*state.ActiveRecord)
	if active.DockerID != "sha256:busybox" {
		t.Errorf("DockerID: got %q, want %q", active.DockerID, "sha256:busybox")
	}
}

func TestPull_Failure_StateGoesFailed(t *testing.T) {
	ops := &fakeOps{
		pullFn: func(_ context.Context, _ string, _ dockerimage.PullOptions) error {
			return errors.New("manifest not found")
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.Pull(context.Background(), "nonexistent:latest", dockerimage.PullOptions{})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusFailed)

	_, rec, _ := st.FindByID(ticket.ChangeID)
	failed := rec.(*state.FailedRecord)
	if failed.Err == "" {
		t.Error("FailedRecord.Err should not be empty")
	}
}

func TestPull_InspectFails_StillActive_WithEmptyDockerID(t *testing.T) {
	// Pull succeeds but the post-pull inspect fails; we treat it as active
	// with an empty DockerID rather than bubbling the inspect error as failure.
	ops := &fakeOps{
		inspFn: func(_ context.Context, _ string) (dockertypes.ImageInspect, error) {
			return dockertypes.ImageInspect{}, errors.New("inspect failed")
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.Pull(context.Background(), "img:latest", dockerimage.PullOptions{})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusActive)

	_, rec, _ := st.FindByID(ticket.ChangeID)
	if rec.(*state.ActiveRecord).DockerID != "" {
		t.Error("expected empty DockerID when inspect fails post-pull")
	}
}

func TestPull_CancelledContextBeforeExecution_StateAbandoned(t *testing.T) {
	// Use a single-worker, zero-size-overflow queue and block it with a
	// slow job so our target job is guaranteed to sit in the queue long
	// enough for us to cancel its context.
	blocker := make(chan struct{})
	ops := &fakeOps{
		pullFn: func(ctx context.Context, ref string, _ dockerimage.PullOptions) error {
			if ref == "__blocker__" {
				<-blocker // hold the worker until we're ready
			}
			return nil
		},
	}
	// 1 worker, queue size 2 so both jobs fit.
	a, st := newActor(t, ops, &actor.Config{Workers: 1, QueueSize: 2})

	// Submit the blocking job first to occupy the single worker.
	_, err := a.Pull(context.Background(), "__blocker__", dockerimage.PullOptions{})
	if err != nil {
		t.Fatalf("blocker pull: %v", err)
	}

	// Submit the target job with an already-cancelled context.
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	ticket, err := a.Pull(cancelledCtx, "target:latest", dockerimage.PullOptions{})
	if err != nil {
		t.Fatalf("target pull: %v", err)
	}

	// Unblock the blocker so the worker moves on to the target job.
	close(blocker)

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusAbandoned)
}

// ── Remove ────────────────────────────────────────────────────────────────────

func TestRemove_Success_StateGoesActive(t *testing.T) {
	a, st := newActor(t, &fakeOps{}, nil)

	ticket, err := a.Remove(context.Background(), "busybox:latest", dockerimage.RemoveOptions{Force: true})
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusActive)
}

func TestRemove_Failure_StateGoesFailed(t *testing.T) {
	ops := &fakeOps{
		removeFn: func(_ context.Context, _ string, _ dockerimage.RemoveOptions) ([]dockerimage.DeleteResponse, error) {
			return nil, errors.New("image in use")
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.Remove(context.Background(), "busy:latest", dockerimage.RemoveOptions{})
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusFailed)
}

// ── Tag ───────────────────────────────────────────────────────────────────────

func TestTag_Success_StateGoesActive(t *testing.T) {
	a, st := newActor(t, &fakeOps{}, nil)

	ticket, err := a.Tag(context.Background(), "busybox:latest", "busybox:stable")
	if err != nil {
		t.Fatalf("Tag: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusActive)
}

func TestTag_Failure_StateGoesFailed(t *testing.T) {
	ops := &fakeOps{
		tagFn: func(_ context.Context, _, _ string) error {
			return errors.New("no such image")
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.Tag(context.Background(), "ghost:latest", "ghost:v1")
	if err != nil {
		t.Fatalf("Tag: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusFailed)
}

// ── Queue full / actor closed ─────────────────────────────────────────────────

func TestQueueFull_ReturnsErrAndAbandons(t *testing.T) {
	blocker := make(chan struct{})
	ops := &fakeOps{
		pullFn: func(ctx context.Context, _ string, _ dockerimage.PullOptions) error {
			<-blocker
			return nil
		},
	}
	// 1 worker, queue size 1.  One item occupies the worker, one fills the queue.
	a, st := newActor(t, ops, &actor.Config{Workers: 1, QueueSize: 1})

	// Occupy the worker.
	_, err := a.Pull(context.Background(), "occupier:latest", dockerimage.PullOptions{})
	if err != nil {
		t.Fatalf("occupier: %v", err)
	}
	// Fill the queue.
	_, err = a.Pull(context.Background(), "queue-filler:latest", dockerimage.PullOptions{})
	if err != nil {
		t.Fatalf("queue-filler: %v", err)
	}

	// This one should be rejected.
	ticket, err := a.Pull(context.Background(), "overflow:latest", dockerimage.PullOptions{})
	if !errors.Is(err, actor.ErrQueueFull) {
		t.Errorf("want ErrQueueFull, got %v", err)
	}
	if ticket != nil {
		t.Error("expected nil ticket on queue-full error")
	}

	// The overflow change should be in the abandoned bucket.
	req, _, fail, abn := st.Summary()
	_ = fail
	if abn < 1 {
		t.Errorf("expected at least 1 abandoned, got req=%d abn=%d", req, abn)
	}

	close(blocker)
}

func TestActorClosed_ReturnsErrAndAbandons(t *testing.T) {
	a, st := newActor(t, &fakeOps{}, nil)
	a.Close()

	ticket, err := a.Pull(context.Background(), "img:latest", dockerimage.PullOptions{})
	if !errors.Is(err, actor.ErrActorClosed) {
		t.Errorf("want ErrActorClosed, got %v", err)
	}
	if ticket != nil {
		t.Error("expected nil ticket after close")
	}

	_, _, _, ab := st.Summary()
	if ab < 1 {
		req, act, fail, abn := st.Summary()
		t.Errorf("expected at least 1 abandoned change, got req=%d act=%d fail=%d abn=%d",
			req, act, fail, abn)
	}
}

func TestClose_DrainsQueueAndAbandonsRemaining(t *testing.T) {
	// Only the first job (the blocker) waits on a channel. Once close(blocker)
	// fires, queued items' fn also returns immediately, so the worker may execute
	// some of them before it sees the closed signal. The only safe invariant is:
	// after Close returns, zero changes remain in the requested bucket.
	blocker := make(chan struct{})
	ops := &fakeOps{
		pullFn: func(_ context.Context, _ string, _ dockerimage.PullOptions) error {
			<-blocker
			return nil
		},
	}
	a, st := newActor(t, ops, &actor.Config{Workers: 1, QueueSize: 10})

	// Occupy the single worker.
	blockerTicket, _ := a.Pull(context.Background(), "blocker:latest", dockerimage.PullOptions{})

	// Enqueue jobs that will sit in the queue while the worker is blocked.
	const queued = 5
	tickets := make([]*actor.Ticket, queued)
	for i := range queued {
		tk, err := a.Pull(context.Background(), fmt.Sprintf("queued-%d:latest", i), dockerimage.PullOptions{})
		if err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
		tickets[i] = tk
	}

	// Unblock the worker shortly after Close is called.
	go func() {
		time.Sleep(10 * time.Millisecond)
		close(blocker)
	}()
	a.Close() // blocks until all workers exit

	// Wait for the blocker ticket to ensure it settled.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = blockerTicket.Wait(ctx)

	// Wait for all queued tickets to settle.
	for i, tk := range tickets {
		tctx, tcancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := tk.Wait(tctx); err != nil {
			t.Errorf("ticket[%d] did not settle: %v", i, err)
		}
		tcancel()
	}

	// After Close, nothing must remain in the requested bucket.
	req, act, fail, abn := st.Summary()
	if req != 0 {
		t.Errorf("requested after close: got %d, want 0 (act=%d fail=%d abn=%d)",
			req, act, fail, abn)
	}
	// All submitted items must have settled somewhere.
	total := 1 + queued // blocker + queued
	if act+fail+abn != total {
		t.Errorf("settled total: got %d, want %d (act=%d fail=%d abn=%d)",
			act+fail+abn, total, act, fail, abn)
	}
}

func TestClose_IsIdempotent(t *testing.T) {
	a := actor.New(&fakeOps{}, state.New(silentLogger()), silentLogger(), nil)
	// Call Close three times — must not panic or deadlock.
	a.Close()
	a.Close()
	a.Close()
}

// ── Read-only operations ──────────────────────────────────────────────────────

func TestList_Async_ReturnsResult(t *testing.T) {
	want := []dockerimage.Summary{{ID: "sha256:abc"}, {ID: "sha256:def"}}
	ops := &fakeOps{
		listFn: func(_ context.Context, _ dockerimage.ListOptions) ([]dockerimage.Summary, error) {
			return want, nil
		},
	}
	a, _ := newActor(t, ops, nil)

	ch := a.List(context.Background(), dockerimage.ListOptions{})

	select {
	case res := <-ch:
		if res.Err != nil {
			t.Fatalf("List: %v", res.Err)
		}
		if len(res.Images) != len(want) {
			t.Errorf("images: got %d, want %d", len(res.Images), len(want))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("List did not return within timeout")
	}
}

func TestList_Async_PropagatesError(t *testing.T) {
	ops := &fakeOps{
		listFn: func(_ context.Context, _ dockerimage.ListOptions) ([]dockerimage.Summary, error) {
			return nil, errors.New("daemon error")
		},
	}
	a, _ := newActor(t, ops, nil)

	res := <-a.List(context.Background(), dockerimage.ListOptions{})
	if res.Err == nil {
		t.Error("expected error from List")
	}
}

func TestInspect_Async_ReturnsResult(t *testing.T) {
	ops := &fakeOps{
		inspFn: func(_ context.Context, id string) (dockertypes.ImageInspect, error) {
			return dockertypes.ImageInspect{ID: id, Os: "linux"}, nil
		},
	}
	a, _ := newActor(t, ops, nil)

	res := <-a.Inspect(context.Background(), "sha256:abc")
	if res.Err != nil {
		t.Fatalf("Inspect: %v", res.Err)
	}
	if res.Info.Os != "linux" {
		t.Errorf("Os: got %q, want %q", res.Info.Os, "linux")
	}
}

func TestExists_Async_ReturnsTrue(t *testing.T) {
	a, _ := newActor(t, &fakeOps{}, nil) // fakeOps.Exists returns true by default

	res := <-a.Exists(context.Background(), "busybox:latest")
	if res.Err != nil {
		t.Fatalf("Exists: %v", res.Err)
	}
	if !res.Present {
		t.Error("expected Present=true")
	}
}

func TestExists_Async_ReturnsFalse(t *testing.T) {
	ops := &fakeOps{
		existsFn: func(_ context.Context, _ string) (bool, error) {
			return false, nil
		},
	}
	a, _ := newActor(t, ops, nil)

	res := <-a.Exists(context.Background(), "ghost:latest")
	if res.Err != nil || res.Present {
		t.Errorf("expected Present=false err=nil, got Present=%v err=%v", res.Present, res.Err)
	}
}

// ── Concurrency ───────────────────────────────────────────────────────────────

func TestConcurrentPulls_AllSettle(t *testing.T) {
	var callCount atomic.Int64
	ops := &fakeOps{
		pullFn: func(_ context.Context, _ string, _ dockerimage.PullOptions) error {
			callCount.Add(1)
			return nil
		},
		inspFn: func(_ context.Context, id string) (dockertypes.ImageInspect, error) {
			return dockertypes.ImageInspect{ID: "sha256:" + id}, nil
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
			t, err := a.Pull(context.Background(),
				fmt.Sprintf("img-%d:latest", i), dockerimage.PullOptions{})
			if err != nil {
				submitErr.Add(1)
				return
			}
			tickets[i] = t
		}()
	}
	wg.Wait()

	if n := submitErr.Load(); n > 0 {
		t.Errorf("%d submit errors", n)
	}

	// Wait for all tickets.
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
		t.Errorf("settled total: got %d, want %d (active=%d failed=%d abandoned=%d)",
			act+fail+abn, total, act, fail, abn)
	}
}

// TestActorClosed_ReturnsErrAndAbandons accesses the summary tuple cleanly.
// (The inline tuple access above was for the flawed version; this is the clean
// reference version kept for the record — the test above was already fixed.)
func TestSummaryHelper(t *testing.T) {
	st := state.New(silentLogger())
	r, a, f, b := st.Summary()
	if r+a+f+b != 0 {
		t.Errorf("fresh state should have all-zero summary")
	}
}
