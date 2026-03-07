package dockercontaineractor_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	actor "dokoko.ai/dokoko/internal/docker/containers/actor"
	state "dokoko.ai/dokoko/internal/docker/containers/state"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"
	dockernetwork "github.com/docker/docker/api/types/network"
)

// ── fakeOps ───────────────────────────────────────────────────────────────────

// fakeOps satisfies the unexported opsProvider interface via field functions.
// Each field defaults to a no-error implementation so tests only set what they
// care about.
type fakeOps struct {
	mu sync.Mutex

	createFn     func(ctx context.Context, name string, config *dockercontainer.Config, hostConfig *dockercontainer.HostConfig, networkConfig *dockernetwork.NetworkingConfig) (dockercontainer.CreateResponse, error)
	startFn      func(ctx context.Context, containerID string, opts dockercontainer.StartOptions) error
	stopFn       func(ctx context.Context, containerID string, opts dockercontainer.StopOptions) error
	removeFn     func(ctx context.Context, containerID string, opts dockercontainer.RemoveOptions) error
	connectFn    func(ctx context.Context, networkID, containerID string, config *dockernetwork.EndpointSettings) error
	disconnectFn func(ctx context.Context, networkID, containerID string, force bool) error
	listFn       func(ctx context.Context, opts dockercontainer.ListOptions) ([]dockertypes.Container, error)
	inspectFn    func(ctx context.Context, containerID string) (dockertypes.ContainerJSON, error)
	existsFn     func(ctx context.Context, containerID string) (bool, error)

	calls []string // names of methods invoked, in order
}

func (f *fakeOps) record(name string) {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	f.mu.Unlock()
}

func (f *fakeOps) Create(ctx context.Context, name string, config *dockercontainer.Config, hostConfig *dockercontainer.HostConfig, networkConfig *dockernetwork.NetworkingConfig) (dockercontainer.CreateResponse, error) {
	f.record("Create")
	if f.createFn != nil {
		return f.createFn(ctx, name, config, hostConfig, networkConfig)
	}
	return dockercontainer.CreateResponse{ID: "fake-container-id"}, nil
}

func (f *fakeOps) Start(ctx context.Context, containerID string, opts dockercontainer.StartOptions) error {
	f.record("Start")
	if f.startFn != nil {
		return f.startFn(ctx, containerID, opts)
	}
	return nil
}

func (f *fakeOps) Stop(ctx context.Context, containerID string, opts dockercontainer.StopOptions) error {
	f.record("Stop")
	if f.stopFn != nil {
		return f.stopFn(ctx, containerID, opts)
	}
	return nil
}

func (f *fakeOps) Remove(ctx context.Context, containerID string, opts dockercontainer.RemoveOptions) error {
	f.record("Remove")
	if f.removeFn != nil {
		return f.removeFn(ctx, containerID, opts)
	}
	return nil
}

func (f *fakeOps) Connect(ctx context.Context, networkID, containerID string, config *dockernetwork.EndpointSettings) error {
	f.record("Connect")
	if f.connectFn != nil {
		return f.connectFn(ctx, networkID, containerID, config)
	}
	return nil
}

func (f *fakeOps) Disconnect(ctx context.Context, networkID, containerID string, force bool) error {
	f.record("Disconnect")
	if f.disconnectFn != nil {
		return f.disconnectFn(ctx, networkID, containerID, force)
	}
	return nil
}

func (f *fakeOps) List(ctx context.Context, opts dockercontainer.ListOptions) ([]dockertypes.Container, error) {
	f.record("List")
	if f.listFn != nil {
		return f.listFn(ctx, opts)
	}
	return nil, nil
}

func (f *fakeOps) Inspect(ctx context.Context, containerID string) (dockertypes.ContainerJSON, error) {
	f.record("Inspect")
	if f.inspectFn != nil {
		return f.inspectFn(ctx, containerID)
	}
	return dockertypes.ContainerJSON{ContainerJSONBase: &dockertypes.ContainerJSONBase{ID: "fake-container-id"}}, nil
}

func (f *fakeOps) Exists(ctx context.Context, containerID string) (bool, error) {
	f.record("Exists")
	if f.existsFn != nil {
		return f.existsFn(ctx, containerID)
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

// containerConfig is a minimal container config for testing.
func containerConfig() *dockercontainer.Config {
	return &dockercontainer.Config{Image: "busybox:latest"}
}

// ── Create ────────────────────────────────────────────────────────────────────

func TestCreate_Success_StateGoesActive(t *testing.T) {
	ops := &fakeOps{
		createFn: func(_ context.Context, _ string, _ *dockercontainer.Config, _ *dockercontainer.HostConfig, _ *dockernetwork.NetworkingConfig) (dockercontainer.CreateResponse, error) {
			return dockercontainer.CreateResponse{ID: "abc123def456"}, nil
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.Create(context.Background(), "my-container", containerConfig(), nil, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if ticket == nil {
		t.Fatal("expected non-nil ticket")
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusActive)

	// DockerID should be set from the create response.
	_, rec, _ := st.FindByID(ticket.ChangeID)
	active := rec.(*state.ActiveRecord)
	if active.DockerID != "abc123def456" {
		t.Errorf("DockerID: got %q, want %q", active.DockerID, "abc123def456")
	}
}

func TestCreate_Failure_StateGoesFailed(t *testing.T) {
	ops := &fakeOps{
		createFn: func(_ context.Context, _ string, _ *dockercontainer.Config, _ *dockercontainer.HostConfig, _ *dockernetwork.NetworkingConfig) (dockercontainer.CreateResponse, error) {
			return dockercontainer.CreateResponse{}, errors.New("no such image")
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.Create(context.Background(), "my-container", containerConfig(), nil, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusFailed)

	_, rec, _ := st.FindByID(ticket.ChangeID)
	failed := rec.(*state.FailedRecord)
	if failed.Err == "" {
		t.Error("FailedRecord.Err should not be empty")
	}
}

func TestCreate_WithNetworkConfig_PassedThrough(t *testing.T) {
	var capturedNetCfg *dockernetwork.NetworkingConfig
	ops := &fakeOps{
		createFn: func(_ context.Context, _ string, _ *dockercontainer.Config, _ *dockercontainer.HostConfig, nc *dockernetwork.NetworkingConfig) (dockercontainer.CreateResponse, error) {
			capturedNetCfg = nc
			return dockercontainer.CreateResponse{ID: "abc123"}, nil
		},
	}
	a, _ := newActor(t, ops, nil)

	netCfg := &dockernetwork.NetworkingConfig{
		EndpointsConfig: map[string]*dockernetwork.EndpointSettings{
			"my-network": {Aliases: []string{"web"}},
		},
	}
	ticket, err := a.Create(context.Background(), "my-container", containerConfig(), nil, netCfg)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	waitTicket(t, ticket)

	if capturedNetCfg == nil {
		t.Fatal("networkConfig was not passed through to ops.Create")
	}
	ep, ok := capturedNetCfg.EndpointsConfig["my-network"]
	if !ok || len(ep.Aliases) == 0 || ep.Aliases[0] != "web" {
		t.Errorf("network endpoint not preserved: %+v", capturedNetCfg)
	}
}

func TestCreate_CancelledContextBeforeExecution_StateAbandoned(t *testing.T) {
	blocker := make(chan struct{})
	var startOnce sync.Once
	started := make(chan struct{})
	ops := &fakeOps{
		createFn: func(ctx context.Context, name string, _ *dockercontainer.Config, _ *dockercontainer.HostConfig, _ *dockernetwork.NetworkingConfig) (dockercontainer.CreateResponse, error) {
			startOnce.Do(func() { close(started) })
			if name == "__blocker__" {
				<-blocker
			}
			return dockercontainer.CreateResponse{ID: "fake"}, nil
		},
	}
	a, st := newActor(t, ops, &actor.Config{Workers: 1, QueueSize: 2})

	_, err := a.Create(context.Background(), "__blocker__", containerConfig(), nil, nil)
	if err != nil {
		t.Fatalf("blocker create: %v", err)
	}
	<-started

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	ticket, err := a.Create(cancelledCtx, "target-container", containerConfig(), nil, nil)
	if err != nil {
		t.Fatalf("target create: %v", err)
	}

	close(blocker)

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusAbandoned)
}

// ── Start ─────────────────────────────────────────────────────────────────────

func TestStart_Success_StateGoesActive(t *testing.T) {
	a, st := newActor(t, &fakeOps{}, nil)

	ticket, err := a.Start(context.Background(), "my-container", dockercontainer.StartOptions{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusActive)
}

func TestStart_Failure_StateGoesFailed(t *testing.T) {
	ops := &fakeOps{
		startFn: func(_ context.Context, _ string, _ dockercontainer.StartOptions) error {
			return errors.New("container already started")
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.Start(context.Background(), "my-container", dockercontainer.StartOptions{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusFailed)
}

// ── Stop ──────────────────────────────────────────────────────────────────────

func TestStop_Success_StateGoesActive(t *testing.T) {
	a, st := newActor(t, &fakeOps{}, nil)

	ticket, err := a.Stop(context.Background(), "my-container", dockercontainer.StopOptions{})
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusActive)
}

func TestStop_Failure_StateGoesFailed(t *testing.T) {
	ops := &fakeOps{
		stopFn: func(_ context.Context, _ string, _ dockercontainer.StopOptions) error {
			return errors.New("no such container")
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.Stop(context.Background(), "ghost-container", dockercontainer.StopOptions{})
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusFailed)
}

// ── Remove ────────────────────────────────────────────────────────────────────

func TestRemove_Success_StateGoesActive(t *testing.T) {
	a, st := newActor(t, &fakeOps{}, nil)

	ticket, err := a.Remove(context.Background(), "my-container", dockercontainer.RemoveOptions{Force: true})
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusActive)
}

func TestRemove_Failure_StateGoesFailed(t *testing.T) {
	ops := &fakeOps{
		removeFn: func(_ context.Context, _ string, _ dockercontainer.RemoveOptions) error {
			return errors.New("container is running")
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.Remove(context.Background(), "running-container", dockercontainer.RemoveOptions{})
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusFailed)
}

// ── Connect ───────────────────────────────────────────────────────────────────

func TestConnect_Success_StateGoesActive(t *testing.T) {
	a, st := newActor(t, &fakeOps{}, nil)

	ticket, err := a.Connect(context.Background(), "my-network", "my-container", nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusActive)
}

func TestConnect_Failure_StateGoesFailed(t *testing.T) {
	ops := &fakeOps{
		connectFn: func(_ context.Context, _, _ string, _ *dockernetwork.EndpointSettings) error {
			return errors.New("network not found")
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.Connect(context.Background(), "ghost-network", "my-container", nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusFailed)
}

func TestConnect_EndpointConfig_PassedThrough(t *testing.T) {
	var capturedAlias []string
	ops := &fakeOps{
		connectFn: func(_ context.Context, _, _ string, cfg *dockernetwork.EndpointSettings) error {
			if cfg != nil {
				capturedAlias = cfg.Aliases
			}
			return nil
		},
	}
	a, _ := newActor(t, ops, nil)

	epCfg := &dockernetwork.EndpointSettings{Aliases: []string{"web", "frontend"}}
	ticket, err := a.Connect(context.Background(), "my-network", "my-container", epCfg)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	waitTicket(t, ticket)

	if len(capturedAlias) != 2 || capturedAlias[0] != "web" {
		t.Errorf("aliases not passed through: %v", capturedAlias)
	}
}

// ── Disconnect ────────────────────────────────────────────────────────────────

func TestDisconnect_Success_StateGoesActive(t *testing.T) {
	a, st := newActor(t, &fakeOps{}, nil)

	ticket, err := a.Disconnect(context.Background(), "my-network", "my-container", false)
	if err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusActive)
}

func TestDisconnect_Failure_StateGoesFailed(t *testing.T) {
	ops := &fakeOps{
		disconnectFn: func(_ context.Context, _, _ string, _ bool) error {
			return errors.New("container not connected to network")
		},
	}
	a, st := newActor(t, ops, nil)

	ticket, err := a.Disconnect(context.Background(), "my-network", "ghost-container", true)
	if err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	waitTicket(t, ticket)
	assertStatus(t, st, ticket.ChangeID, state.StatusFailed)
}

func TestDisconnect_ForceFlag_PassedThrough(t *testing.T) {
	var capturedForce bool
	ops := &fakeOps{
		disconnectFn: func(_ context.Context, _, _ string, force bool) error {
			capturedForce = force
			return nil
		},
	}
	a, _ := newActor(t, ops, nil)

	ticket, err := a.Disconnect(context.Background(), "my-network", "my-container", true)
	if err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	waitTicket(t, ticket)

	if !capturedForce {
		t.Error("force=true was not passed through to ops.Disconnect")
	}
}

// ── Queue full / actor closed ─────────────────────────────────────────────────

func TestQueueFull_ReturnsErrAndAbandons(t *testing.T) {
	blocker := make(chan struct{})
	started := make(chan struct{})
	var startOnce sync.Once
	ops := &fakeOps{
		createFn: func(ctx context.Context, name string, _ *dockercontainer.Config, _ *dockercontainer.HostConfig, _ *dockernetwork.NetworkingConfig) (dockercontainer.CreateResponse, error) {
			startOnce.Do(func() { close(started) }) // signal: worker picked up occupier
			<-blocker
			return dockercontainer.CreateResponse{ID: "fake"}, nil
		},
	}
	// 1 worker, queue size 1.  One item occupies the worker, one fills the queue.
	a, st := newActor(t, ops, &actor.Config{Workers: 1, QueueSize: 1})

	// Occupy the worker.
	_, err := a.Create(context.Background(), "occupier", containerConfig(), nil, nil)
	if err != nil {
		t.Fatalf("occupier: %v", err)
	}
	// Wait until the worker has actually picked up the occupier so the queue is empty.
	<-started
	// Fill the queue.
	_, err = a.Create(context.Background(), "queue-filler", containerConfig(), nil, nil)
	if err != nil {
		t.Fatalf("queue-filler: %v", err)
	}

	// This one should be rejected.
	ticket, err := a.Create(context.Background(), "overflow", containerConfig(), nil, nil)
	if !errors.Is(err, actor.ErrQueueFull) {
		t.Errorf("want ErrQueueFull, got %v", err)
	}
	if ticket != nil {
		t.Error("expected nil ticket on queue-full error")
	}

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

	ticket, err := a.Create(context.Background(), "my-container", containerConfig(), nil, nil)
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
	blocker := make(chan struct{})
	ops := &fakeOps{
		createFn: func(_ context.Context, _ string, _ *dockercontainer.Config, _ *dockercontainer.HostConfig, _ *dockernetwork.NetworkingConfig) (dockercontainer.CreateResponse, error) {
			<-blocker
			return dockercontainer.CreateResponse{ID: "fake"}, nil
		},
	}
	a, st := newActor(t, ops, &actor.Config{Workers: 1, QueueSize: 10})

	blockerTicket, _ := a.Create(context.Background(), "blocker", containerConfig(), nil, nil)

	const queued = 5
	tickets := make([]*actor.Ticket, queued)
	for i := range queued {
		tk, err := a.Create(context.Background(), fmt.Sprintf("queued-%d", i), containerConfig(), nil, nil)
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

// ── Read-only operations ──────────────────────────────────────────────────────

func TestList_Async_ReturnsResult(t *testing.T) {
	want := []dockertypes.Container{{ID: "abc123"}, {ID: "def456"}}
	ops := &fakeOps{
		listFn: func(_ context.Context, _ dockercontainer.ListOptions) ([]dockertypes.Container, error) {
			return want, nil
		},
	}
	a, _ := newActor(t, ops, nil)

	ch := a.List(context.Background(), dockercontainer.ListOptions{})

	select {
	case res := <-ch:
		if res.Err != nil {
			t.Fatalf("List: %v", res.Err)
		}
		if len(res.Containers) != len(want) {
			t.Errorf("containers: got %d, want %d", len(res.Containers), len(want))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("List did not return within timeout")
	}
}

func TestList_Async_PropagatesError(t *testing.T) {
	ops := &fakeOps{
		listFn: func(_ context.Context, _ dockercontainer.ListOptions) ([]dockertypes.Container, error) {
			return nil, errors.New("daemon error")
		},
	}
	a, _ := newActor(t, ops, nil)

	res := <-a.List(context.Background(), dockercontainer.ListOptions{})
	if res.Err == nil {
		t.Error("expected error from List")
	}
}

func TestInspect_Async_ReturnsResult(t *testing.T) {
	ops := &fakeOps{
		inspectFn: func(_ context.Context, id string) (dockertypes.ContainerJSON, error) {
			return dockertypes.ContainerJSON{
				ContainerJSONBase: &dockertypes.ContainerJSONBase{
					ID:    id,
					State: &dockertypes.ContainerState{Running: true},
				},
			}, nil
		},
	}
	a, _ := newActor(t, ops, nil)

	res := <-a.Inspect(context.Background(), "abc123")
	if res.Err != nil {
		t.Fatalf("Inspect: %v", res.Err)
	}
	if res.Info.ID != "abc123" {
		t.Errorf("ID: got %q, want %q", res.Info.ID, "abc123")
	}
}

func TestExists_Async_ReturnsTrue(t *testing.T) {
	a, _ := newActor(t, &fakeOps{}, nil) // fakeOps.Exists returns true by default

	res := <-a.Exists(context.Background(), "my-container")
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

	res := <-a.Exists(context.Background(), "ghost-container")
	if res.Err != nil || res.Present {
		t.Errorf("expected Present=false err=nil, got Present=%v err=%v", res.Present, res.Err)
	}
}

// ── Concurrency ───────────────────────────────────────────────────────────────

func TestConcurrentCreates_AllSettle(t *testing.T) {
	var callCount atomic.Int64
	ops := &fakeOps{
		createFn: func(_ context.Context, _ string, _ *dockercontainer.Config, _ *dockercontainer.HostConfig, _ *dockernetwork.NetworkingConfig) (dockercontainer.CreateResponse, error) {
			callCount.Add(1)
			return dockercontainer.CreateResponse{ID: "fake"}, nil
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
				fmt.Sprintf("container-%d", i), containerConfig(), nil, nil)
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
		t.Errorf("settled total: got %d, want %d (active=%d failed=%d abandoned=%d)",
			act+fail+abn, total, act, fail, abn)
	}
}

func TestSummaryHelper(t *testing.T) {
	st := state.New(silentLogger())
	r, a, f, b := st.Summary()
	if r+a+f+b != 0 {
		t.Errorf("fresh state should have all-zero summary")
	}
}
