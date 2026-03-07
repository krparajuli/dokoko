package dockerbuildactor_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	actor "dokoko.ai/dokoko/internal/docker/builds/actor"
	dockerbuildops "dokoko.ai/dokoko/internal/docker/builds/ops"
	state "dokoko.ai/dokoko/internal/docker/builds/state"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
)

// ── fakeOps ───────────────────────────────────────────────────────────────────

type fakeOps struct {
	mu sync.Mutex

	buildFn      func(ctx context.Context, req dockerbuildops.BuildRequest) (dockerbuildops.BuildResponse, error)
	pruneCacheFn func(ctx context.Context, opts dockertypes.BuildCachePruneOptions) (*dockertypes.BuildCachePruneReport, error)

	calls []string
}

func (f *fakeOps) record(name string) {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	f.mu.Unlock()
}

func (f *fakeOps) Build(ctx context.Context, req dockerbuildops.BuildRequest) (dockerbuildops.BuildResponse, error) {
	f.record("Build")
	if f.buildFn != nil {
		return f.buildFn(ctx, req)
	}
	tag := ""
	if len(req.Tags) > 0 {
		tag = req.Tags[0]
	}
	return dockerbuildops.BuildResponse{ImageID: "sha256:" + tag, Log: nil}, nil
}

func (f *fakeOps) PruneCache(ctx context.Context, opts dockertypes.BuildCachePruneOptions) (*dockertypes.BuildCachePruneReport, error) {
	f.record("PruneCache")
	if f.pruneCacheFn != nil {
		return f.pruneCacheFn(ctx, opts)
	}
	return &dockertypes.BuildCachePruneReport{}, nil
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

// ── Build ─────────────────────────────────────────────────────────────────────

func TestBuild_Success_StateGoesActive(t *testing.T) {
	ops := &fakeOps{
		buildFn: func(_ context.Context, req dockerbuildops.BuildRequest) (dockerbuildops.BuildResponse, error) {
			return dockerbuildops.BuildResponse{ImageID: "sha256:deadbeef1234", Log: []string{"Step 1: done"}}, nil
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.Build(context.Background(), dockerbuildops.BuildRequest{
		ContextDir: "/tmp",
		Tags:       []string{"myapp:1.0"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if ticket == nil {
		t.Fatal("expected non-nil ticket")
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusActive)

	_, rec, _ := st.FindByID(ticket.ChangeID)
	active := rec.(*state.ActiveRecord)
	if active.ImageID != "sha256:deadbeef1234" {
		t.Errorf("ImageID: got %q, want %q", active.ImageID, "sha256:deadbeef1234")
	}
}

func TestBuild_Failure_StateGoesFailed(t *testing.T) {
	ops := &fakeOps{
		buildFn: func(_ context.Context, _ dockerbuildops.BuildRequest) (dockerbuildops.BuildResponse, error) {
			return dockerbuildops.BuildResponse{}, errors.New("Dockerfile not found")
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.Build(context.Background(), dockerbuildops.BuildRequest{
		ContextDir: "/nonexistent",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusFailed)

	_, rec, _ := st.FindByID(ticket.ChangeID)
	if rec.(*state.FailedRecord).Err == "" {
		t.Error("FailedRecord.Err should not be empty")
	}
}

func TestBuild_MultipleTagsRecordedInState(t *testing.T) {
	ops := &fakeOps{}
	a, st := newActor(t, ops, nil)

	ticket, err := a.Build(context.Background(), dockerbuildops.BuildRequest{
		ContextDir: "/tmp",
		Tags:       []string{"myapp:1.0", "myapp:latest"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	waitTicket(t, ticket)

	_, rec, _ := st.FindByID(ticket.ChangeID)
	change := rec.(*state.ActiveRecord).Change
	if change.Tags != "myapp:1.0,myapp:latest" {
		t.Errorf("Tags: got %q, want %q", change.Tags, "myapp:1.0,myapp:latest")
	}
}

func TestBuild_RemoteContext_MetaRecorded(t *testing.T) {
	ops := &fakeOps{}
	a, st := newActor(t, ops, nil)

	remoteURL := "https://github.com/example/repo.git"
	ticket, err := a.Build(context.Background(), dockerbuildops.BuildRequest{
		RemoteContext: remoteURL,
		Tags:          []string{"built-from-git:latest"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	waitTicket(t, ticket)

	_, rec, _ := st.FindByID(ticket.ChangeID)
	change := rec.(*state.ActiveRecord).Change
	if change.Meta["remote_context"] != remoteURL {
		t.Errorf("Meta[remote_context]: got %q, want %q", change.Meta["remote_context"], remoteURL)
	}
}

// ── PruneCache ────────────────────────────────────────────────────────────────

func TestPruneCache_Success_StateGoesActive(t *testing.T) {
	ops := &fakeOps{
		pruneCacheFn: func(_ context.Context, _ dockertypes.BuildCachePruneOptions) (*dockertypes.BuildCachePruneReport, error) {
			return &dockertypes.BuildCachePruneReport{
				CachesDeleted:  []string{"abc123", "def456"},
				SpaceReclaimed: 2048,
			}, nil
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.PruneCache(context.Background(), dockertypes.BuildCachePruneOptions{})
	if err != nil {
		t.Fatalf("PruneCache: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusActive)
}

func TestPruneCache_Failure_StateGoesFailed(t *testing.T) {
	ops := &fakeOps{
		pruneCacheFn: func(_ context.Context, _ dockertypes.BuildCachePruneOptions) (*dockertypes.BuildCachePruneReport, error) {
			return nil, errors.New("daemon unavailable")
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.PruneCache(context.Background(), dockertypes.BuildCachePruneOptions{})
	if err != nil {
		t.Fatalf("PruneCache: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusFailed)
}

// ── Queue full / actor closed ─────────────────────────────────────────────────

func TestQueueFull_ReturnsErrAndAbandons(t *testing.T) {
	blocker := make(chan struct{})
	workerStarted := make(chan struct{}, 1)
	ops := &fakeOps{
		buildFn: func(ctx context.Context, _ dockerbuildops.BuildRequest) (dockerbuildops.BuildResponse, error) {
			select {
			case workerStarted <- struct{}{}:
			default:
			}
			<-blocker
			return dockerbuildops.BuildResponse{}, nil
		},
	}
	a, st := newActor(t, ops, &actor.Config{Workers: 1, QueueSize: 1})

	// Occupy the worker.
	_, err := a.Build(context.Background(), dockerbuildops.BuildRequest{ContextDir: "/tmp", Tags: []string{"occupier"}})
	if err != nil {
		t.Fatalf("occupier: %v", err)
	}
	// Wait until the worker has actually dequeued "occupier".
	<-workerStarted
	// Fill the queue.
	_, err = a.Build(context.Background(), dockerbuildops.BuildRequest{ContextDir: "/tmp", Tags: []string{"filler"}})
	if err != nil {
		t.Fatalf("queue-filler: %v", err)
	}

	// This one should be rejected.
	ticket, err := a.Build(context.Background(), dockerbuildops.BuildRequest{ContextDir: "/tmp", Tags: []string{"overflow"}})
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

	ticket, err := a.Build(context.Background(), dockerbuildops.BuildRequest{ContextDir: "/tmp"})
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
		buildFn: func(_ context.Context, _ dockerbuildops.BuildRequest) (dockerbuildops.BuildResponse, error) {
			<-blocker
			return dockerbuildops.BuildResponse{}, nil
		},
	}
	a, st := newActor(t, ops, &actor.Config{Workers: 1, QueueSize: 10})

	blockerTicket, _ := a.Build(context.Background(), dockerbuildops.BuildRequest{
		ContextDir: "/tmp", Tags: []string{"blocker"},
	})

	const queued = 5
	tickets := make([]*actor.Ticket, queued)
	for i := range queued {
		tk, err := a.Build(context.Background(), dockerbuildops.BuildRequest{
			ContextDir: "/tmp",
			Tags:       []string{fmt.Sprintf("queued-%d", i)},
		})
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
		buildFn: func(ctx context.Context, req dockerbuildops.BuildRequest) (dockerbuildops.BuildResponse, error) {
			if len(req.Tags) > 0 && req.Tags[0] == "__blocker__" {
				<-blocker
			}
			return dockerbuildops.BuildResponse{}, nil
		},
	}
	a, st := newActor(t, ops, &actor.Config{Workers: 1, QueueSize: 2})

	_, err := a.Build(context.Background(), dockerbuildops.BuildRequest{
		ContextDir: "/tmp", Tags: []string{"__blocker__"},
	})
	if err != nil {
		t.Fatalf("blocker build: %v", err)
	}

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	ticket, err := a.Build(cancelledCtx, dockerbuildops.BuildRequest{
		ContextDir: "/tmp", Tags: []string{"target"},
	})
	if err != nil {
		t.Fatalf("target build: %v", err)
	}

	close(blocker)

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusAbandoned)
}

// ── Concurrency ───────────────────────────────────────────────────────────────

func TestConcurrentBuilds_AllSettle(t *testing.T) {
	var callCount atomic.Int64
	ops := &fakeOps{
		buildFn: func(_ context.Context, req dockerbuildops.BuildRequest) (dockerbuildops.BuildResponse, error) {
			callCount.Add(1)
			tag := ""
			if len(req.Tags) > 0 {
				tag = req.Tags[0]
			}
			return dockerbuildops.BuildResponse{ImageID: "sha256:" + tag}, nil
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
			tk, err := a.Build(context.Background(), dockerbuildops.BuildRequest{
				ContextDir: "/tmp",
				Tags:       []string{fmt.Sprintf("app:%d", i)},
			})
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
