package dockercontainerexecactor_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	actor "dokoko.ai/dokoko/internal/docker/containerexec/actor"
	state "dokoko.ai/dokoko/internal/docker/containerexec/state"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"
)

// ── fakeOps ───────────────────────────────────────────────────────────────────

type fakeOps struct {
	mu sync.Mutex

	createFn  func(ctx context.Context, containerID string, config dockertypes.ExecConfig) (dockertypes.IDResponse, error)
	startFn   func(ctx context.Context, execID string, config dockertypes.ExecStartCheck) error
	attachFn  func(ctx context.Context, execID string, config dockertypes.ExecStartCheck) (dockertypes.HijackedResponse, error)
	inspectFn func(ctx context.Context, execID string) (dockertypes.ContainerExecInspect, error)
	resizeFn  func(ctx context.Context, execID string, opts dockercontainer.ResizeOptions) error

	calls []string
}

func (f *fakeOps) record(op string) {
	f.mu.Lock()
	f.calls = append(f.calls, op)
	f.mu.Unlock()
}

func (f *fakeOps) Create(ctx context.Context, containerID string, config dockertypes.ExecConfig) (dockertypes.IDResponse, error) {
	f.record("create")
	if f.createFn != nil {
		return f.createFn(ctx, containerID, config)
	}
	return dockertypes.IDResponse{ID: "fake-exec-id"}, nil
}

func (f *fakeOps) Start(ctx context.Context, execID string, config dockertypes.ExecStartCheck) error {
	f.record("start")
	if f.startFn != nil {
		return f.startFn(ctx, execID, config)
	}
	return nil
}

func (f *fakeOps) Attach(ctx context.Context, execID string, config dockertypes.ExecStartCheck) (dockertypes.HijackedResponse, error) {
	f.record("attach")
	if f.attachFn != nil {
		return f.attachFn(ctx, execID, config)
	}
	return dockertypes.HijackedResponse{}, nil
}

func (f *fakeOps) Inspect(ctx context.Context, execID string) (dockertypes.ContainerExecInspect, error) {
	f.record("inspect")
	if f.inspectFn != nil {
		return f.inspectFn(ctx, execID)
	}
	return dockertypes.ContainerExecInspect{ExecID: execID, Running: false, ExitCode: 0}, nil
}

func (f *fakeOps) Resize(ctx context.Context, execID string, opts dockercontainer.ResizeOptions) error {
	f.record("resize")
	if f.resizeFn != nil {
		return f.resizeFn(ctx, execID, opts)
	}
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func newActor(t *testing.T, ops *fakeOps, cfg *actor.Config) (*actor.Actor, *state.State) {
	t.Helper()
	log := logger.New(logger.LevelSilent)
	st := state.New(log)
	a := actor.New(ops, st, log, cfg)
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
		t.Fatalf("FindByID %s: %v", changeID, err)
	}
	if got != want {
		t.Errorf("want status=%s got %s", want, got)
	}
}

// ── ExecCreate ────────────────────────────────────────────────────────────────

func TestCreate_Success_StateGoesActive(t *testing.T) {
	a, st := newActor(t, &fakeOps{}, nil)

	ticket, err := a.Create(context.Background(), "ctr-abc", dockertypes.ExecConfig{Cmd: []string{"ls"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusActive)

	// Verify exec ID was stored in the active record.
	active := st.Active()
	if len(active) != 1 || active[0].ExecID != "fake-exec-id" {
		t.Errorf("expected active record with ExecID=fake-exec-id, got %v", active)
	}
}

func TestCreate_Failure_StateGoesFailed(t *testing.T) {
	ops := &fakeOps{
		createFn: func(_ context.Context, _ string, _ dockertypes.ExecConfig) (dockertypes.IDResponse, error) {
			return dockertypes.IDResponse{}, errors.New("container not running")
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.Create(context.Background(), "stopped-ctr", dockertypes.ExecConfig{Cmd: []string{"ls"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusFailed)
}

func TestCreate_CancelledContextBeforeExecution_StateAbandoned(t *testing.T) {
	blocker := make(chan struct{})
	ops := &fakeOps{
		createFn: func(_ context.Context, _ string, _ dockertypes.ExecConfig) (dockertypes.IDResponse, error) {
			<-blocker
			return dockertypes.IDResponse{ID: "exec-id"}, nil
		},
	}
	a, st := newActor(t, ops, &actor.Config{Workers: 1, QueueSize: 10})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before submitting

	ticket, err := a.Create(ctx, "ctr", dockertypes.ExecConfig{Cmd: []string{"ls"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusAbandoned)
	close(blocker)
}

func TestCreate_ExecIDPassedToState(t *testing.T) {
	ops := &fakeOps{
		createFn: func(_ context.Context, _ string, _ dockertypes.ExecConfig) (dockertypes.IDResponse, error) {
			return dockertypes.IDResponse{ID: "specific-exec-id"}, nil
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.Create(context.Background(), "ctr", dockertypes.ExecConfig{Cmd: []string{"sh"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	waitTicket(t, ticket)

	active := st.Active()
	if len(active) != 1 {
		t.Fatalf("expected 1 active record, got %d", len(active))
	}
	if active[0].ExecID != "specific-exec-id" {
		t.Errorf("want ExecID=%q got %q", "specific-exec-id", active[0].ExecID)
	}
}

func TestCreate_ConfigPassedThrough(t *testing.T) {
	var gotConfig dockertypes.ExecConfig
	ops := &fakeOps{
		createFn: func(_ context.Context, _ string, config dockertypes.ExecConfig) (dockertypes.IDResponse, error) {
			gotConfig = config
			return dockertypes.IDResponse{ID: "exec-id"}, nil
		},
	}
	a, _ := newActor(t, ops, nil)

	cfg := dockertypes.ExecConfig{
		Cmd:          []string{"echo", "hello"},
		User:         "root",
		Tty:          true,
		AttachStdout: true,
	}
	ticket, err := a.Create(context.Background(), "ctr", cfg)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	waitTicket(t, ticket)

	if gotConfig.User != "root" || !gotConfig.Tty || !gotConfig.AttachStdout {
		t.Errorf("config not passed through: %+v", gotConfig)
	}
	if len(gotConfig.Cmd) != 2 || gotConfig.Cmd[0] != "echo" {
		t.Errorf("cmd not passed through: %v", gotConfig.Cmd)
	}
}

// ── ExecStart ─────────────────────────────────────────────────────────────────

func TestStart_Success_StateGoesActive(t *testing.T) {
	a, st := newActor(t, &fakeOps{}, nil)

	ticket, err := a.Start(context.Background(), "exec-abc", dockertypes.ExecStartCheck{Detach: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusActive)
}

func TestStart_Failure_StateGoesFailed(t *testing.T) {
	ops := &fakeOps{
		startFn: func(_ context.Context, _ string, _ dockertypes.ExecStartCheck) error {
			return errors.New("exec not found")
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.Start(context.Background(), "bad-exec-id", dockertypes.ExecStartCheck{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusFailed)
}

func TestStart_DetachFlag_PassedThrough(t *testing.T) {
	var gotConfig dockertypes.ExecStartCheck
	ops := &fakeOps{
		startFn: func(_ context.Context, _ string, config dockertypes.ExecStartCheck) error {
			gotConfig = config
			return nil
		},
	}
	a, _ := newActor(t, ops, nil)

	ticket, err := a.Start(context.Background(), "exec-id", dockertypes.ExecStartCheck{Detach: true, Tty: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitTicket(t, ticket)

	if !gotConfig.Detach || !gotConfig.Tty {
		t.Errorf("config not passed through: detach=%v tty=%v", gotConfig.Detach, gotConfig.Tty)
	}
}

// ── ExecResize ────────────────────────────────────────────────────────────────

func TestResize_Success_StateGoesActive(t *testing.T) {
	a, st := newActor(t, &fakeOps{}, nil)

	ticket, err := a.Resize(context.Background(), "exec-abc", dockercontainer.ResizeOptions{Height: 40, Width: 120})
	if err != nil {
		t.Fatalf("Resize: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusActive)
}

func TestResize_Failure_StateGoesFailed(t *testing.T) {
	ops := &fakeOps{
		resizeFn: func(_ context.Context, _ string, _ dockercontainer.ResizeOptions) error {
			return errors.New("exec is not running")
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.Resize(context.Background(), "dead-exec", dockercontainer.ResizeOptions{Height: 24, Width: 80})
	if err != nil {
		t.Fatalf("Resize: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusFailed)
}

func TestResize_Dimensions_PassedThrough(t *testing.T) {
	var gotOpts dockercontainer.ResizeOptions
	ops := &fakeOps{
		resizeFn: func(_ context.Context, _ string, opts dockercontainer.ResizeOptions) error {
			gotOpts = opts
			return nil
		},
	}
	a, _ := newActor(t, ops, nil)

	ticket, err := a.Resize(context.Background(), "exec-id", dockercontainer.ResizeOptions{Height: 50, Width: 200})
	if err != nil {
		t.Fatalf("Resize: %v", err)
	}
	waitTicket(t, ticket)

	if gotOpts.Height != 50 || gotOpts.Width != 200 {
		t.Errorf("dimensions not passed through: h=%d w=%d", gotOpts.Height, gotOpts.Width)
	}
}

// ── Inspect (read-only) ───────────────────────────────────────────────────────

func TestInspect_Async_ReturnsResult(t *testing.T) {
	ops := &fakeOps{
		inspectFn: func(_ context.Context, execID string) (dockertypes.ContainerExecInspect, error) {
			return dockertypes.ContainerExecInspect{
				ExecID:   execID,
				Running:  false,
				ExitCode: 42,
			}, nil
		},
	}
	a, _ := newActor(t, ops, nil)

	ch := a.Inspect(context.Background(), "exec-xyz")
	result := <-ch
	if result.Err != nil {
		t.Fatalf("Inspect: %v", result.Err)
	}
	if result.Info.ExitCode != 42 {
		t.Errorf("want ExitCode=42, got %d", result.Info.ExitCode)
	}
	if result.Info.ExecID != "exec-xyz" {
		t.Errorf("want ExecID=%q got %q", "exec-xyz", result.Info.ExecID)
	}
}

func TestInspect_Async_PropagatesError(t *testing.T) {
	ops := &fakeOps{
		inspectFn: func(_ context.Context, _ string) (dockertypes.ContainerExecInspect, error) {
			return dockertypes.ContainerExecInspect{}, errors.New("exec not found")
		},
	}
	a, _ := newActor(t, ops, nil)

	ch := a.Inspect(context.Background(), "ghost-exec")
	result := <-ch
	if result.Err == nil {
		t.Error("expected error from Inspect, got nil")
	}
}

// ── Attach (read-only) ────────────────────────────────────────────────────────

func TestAttach_Async_ReturnsHijack(t *testing.T) {
	ops := &fakeOps{
		attachFn: func(_ context.Context, _ string, _ dockertypes.ExecStartCheck) (dockertypes.HijackedResponse, error) {
			return dockertypes.HijackedResponse{}, nil
		},
	}
	a, _ := newActor(t, ops, nil)

	ch := a.Attach(context.Background(), "exec-xyz", dockertypes.ExecStartCheck{Tty: true})
	result := <-ch
	if result.Err != nil {
		t.Fatalf("Attach: %v", result.Err)
	}
}

func TestAttach_Async_PropagatesError(t *testing.T) {
	ops := &fakeOps{
		attachFn: func(_ context.Context, _ string, _ dockertypes.ExecStartCheck) (dockertypes.HijackedResponse, error) {
			return dockertypes.HijackedResponse{}, errors.New("exec already running")
		},
	}
	a, _ := newActor(t, ops, nil)

	ch := a.Attach(context.Background(), "exec-xyz", dockertypes.ExecStartCheck{})
	result := <-ch
	if result.Err == nil {
		t.Error("expected error from Attach, got nil")
	}
}

func TestAttach_ConfigPassedThrough(t *testing.T) {
	var gotConfig dockertypes.ExecStartCheck
	ops := &fakeOps{
		attachFn: func(_ context.Context, _ string, config dockertypes.ExecStartCheck) (dockertypes.HijackedResponse, error) {
			gotConfig = config
			return dockertypes.HijackedResponse{}, nil
		},
	}
	a, _ := newActor(t, ops, nil)

	ch := a.Attach(context.Background(), "exec-id", dockertypes.ExecStartCheck{Tty: true, Detach: false})
	<-ch

	if !gotConfig.Tty || gotConfig.Detach {
		t.Errorf("config not passed through: tty=%v detach=%v", gotConfig.Tty, gotConfig.Detach)
	}
}

// ── Queue full / actor closed ─────────────────────────────────────────────────

func TestQueueFull_ReturnsErrAndAbandons(t *testing.T) {
	blocker := make(chan struct{})
	started := make(chan struct{})
	var startOnce sync.Once
	ops := &fakeOps{
		createFn: func(_ context.Context, _ string, _ dockertypes.ExecConfig) (dockertypes.IDResponse, error) {
			startOnce.Do(func() { close(started) })
			<-blocker
			return dockertypes.IDResponse{ID: "exec-id"}, nil
		},
	}
	// 1 worker, queue size 1: one item occupies the worker, one fills the queue.
	a, st := newActor(t, ops, &actor.Config{Workers: 1, QueueSize: 1})

	_, err := a.Create(context.Background(), "ctr", dockertypes.ExecConfig{Cmd: []string{"ls"}})
	if err != nil {
		t.Fatalf("occupier: %v", err)
	}
	<-started // wait until worker has dequeued the occupier

	_, err = a.Create(context.Background(), "ctr", dockertypes.ExecConfig{Cmd: []string{"ls"}})
	if err != nil {
		t.Fatalf("queue-filler: %v", err)
	}

	ticket, err := a.Create(context.Background(), "ctr", dockertypes.ExecConfig{Cmd: []string{"ls"}})
	if !errors.Is(err, actor.ErrQueueFull) {
		t.Errorf("want ErrQueueFull, got %v", err)
	}
	if ticket != nil {
		t.Error("expected nil ticket on queue-full error")
	}

	_, _, _, abn := st.Summary()
	if abn < 1 {
		t.Errorf("expected at least 1 abandoned, got %d", abn)
	}

	close(blocker)
}

func TestActorClosed_ReturnsErrAndAbandons(t *testing.T) {
	a, st := newActor(t, &fakeOps{}, nil)
	a.Close()

	ticket, err := a.Create(context.Background(), "ctr", dockertypes.ExecConfig{Cmd: []string{"ls"}})
	if !errors.Is(err, actor.ErrActorClosed) {
		t.Errorf("want ErrActorClosed, got %v", err)
	}
	if ticket != nil {
		t.Error("expected nil ticket after close")
	}

	_, _, _, abn := st.Summary()
	if abn < 1 {
		t.Errorf("expected at least 1 abandoned change, got %d", abn)
	}
}

func TestClose_DrainsQueueAndAbandonsRemaining(t *testing.T) {
	blocker := make(chan struct{})
	ops := &fakeOps{
		createFn: func(_ context.Context, _ string, _ dockertypes.ExecConfig) (dockertypes.IDResponse, error) {
			<-blocker
			return dockertypes.IDResponse{ID: "exec-id"}, nil
		},
	}
	a, st := newActor(t, ops, &actor.Config{Workers: 1, QueueSize: 10})

	blockerTicket, _ := a.Create(context.Background(), "ctr", dockertypes.ExecConfig{Cmd: []string{"ls"}})

	const queued = 5
	tickets := make([]*actor.Ticket, queued)
	for i := range queued {
		tk, err := a.Create(context.Background(), "ctr", dockertypes.ExecConfig{Cmd: []string{"ls"}})
		if err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
		tickets[i] = tk
	}

	// Close blocks until all workers exit. Unblock the in-flight item in a
	// goroutine so the worker can finish and drain the queue.
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
		t.Errorf("expected 0 requested after close, got %d (act=%d fail=%d abn=%d)", req, act, fail, abn)
	}
	total := 1 + queued
	if act+fail+abn != total {
		t.Errorf("settled total: got %d, want %d (act=%d fail=%d abn=%d)", act+fail+abn, total, act, fail, abn)
	}
}

func TestClose_IsIdempotent(t *testing.T) {
	a, _ := newActor(t, &fakeOps{}, nil)
	a.Close()
	a.Close() // second call must not panic
}

// ── Concurrent creates ────────────────────────────────────────────────────────

func TestConcurrentCreates_AllSettle(t *testing.T) {
	a, st := newActor(t, &fakeOps{}, nil)

	const n = 20
	tickets := make([]*actor.Ticket, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tk, err := a.Create(context.Background(), "ctr", dockertypes.ExecConfig{Cmd: []string{"ls"}})
			if err != nil {
				return
			}
			tickets[i] = tk
		}(i)
	}
	wg.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, tk := range tickets {
		if tk != nil {
			_ = tk.Wait(ctx)
		}
	}

	req, _, _, _ := st.Summary()
	if req != 0 {
		t.Errorf("expected 0 requested after all settle, got %d", req)
	}
}
