// Package dockercontaineractor dispatches Docker container operations
// asynchronously, keeping dockercontainerstate in sync with every outcome.
//
// Flow for mutating operations (Create, Start, Stop, Remove, Connect, Disconnect):
//
//	caller → Actor.Create()
//	            → state.RequestChange()       [immediate, on caller goroutine]
//	            → enqueue work item           [immediate, non-blocking]
//	            ← *Ticket{ChangeID, Done}     [returned to caller immediately]
//
//	            … worker goroutine picks up work …
//	            → ops.Create()
//	            → state.ConfirmSuccess()  (on success)
//	            → state.RecordFailure()   (on ops error)
//	            → state.Abandon()         (if context was cancelled before execution)
//	            → close(Ticket.Done)
//
// Read-only operations (List, Inspect, Exists) bypass state and fire a single
// goroutine each, returning a buffered result channel so the caller is never
// forced to block.
package dockercontaineractor

import (
	"context"
	"errors"
	"fmt"
	"sync"

	dockercontainerstate "dokoko.ai/dokoko/internal/docker/containers/state"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"
	dockernetwork "github.com/docker/docker/api/types/network"
)

// ── Errors ────────────────────────────────────────────────────────────────────

var (
	// ErrQueueFull is returned when the internal work queue is at capacity.
	ErrQueueFull = errors.New("actor work queue full")

	// ErrActorClosed is returned when a mutating operation is submitted to a
	// stopped actor.
	ErrActorClosed = errors.New("actor is closed")
)

// ── Configuration ─────────────────────────────────────────────────────────────

// Config controls Actor behaviour.  All fields have sane defaults.
type Config struct {
	// Workers is the number of goroutines that drain the work queue.
	// Default: 4.
	Workers int

	// QueueSize is the maximum number of pending items before submits are
	// abandoned immediately.  Default: 64.
	QueueSize int
}

func (c *Config) withDefaults() Config {
	out := Config{Workers: 4, QueueSize: 64}
	if c != nil {
		if c.Workers > 0 {
			out.Workers = c.Workers
		}
		if c.QueueSize > 0 {
			out.QueueSize = c.QueueSize
		}
	}
	return out
}

// ── opsProvider interface ─────────────────────────────────────────────────────

// opsProvider is the subset of *dockercontainerops.Ops that the Actor uses.
// Declared here so tests can inject a fake without a real Docker daemon.
type opsProvider interface {
	Create(ctx context.Context, name string, config *dockercontainer.Config, hostConfig *dockercontainer.HostConfig, networkConfig *dockernetwork.NetworkingConfig) (dockercontainer.CreateResponse, error)
	Start(ctx context.Context, containerID string, opts dockercontainer.StartOptions) error
	Stop(ctx context.Context, containerID string, opts dockercontainer.StopOptions) error
	Remove(ctx context.Context, containerID string, opts dockercontainer.RemoveOptions) error
	Connect(ctx context.Context, networkID, containerID string, config *dockernetwork.EndpointSettings) error
	Disconnect(ctx context.Context, networkID, containerID string, force bool) error
	List(ctx context.Context, opts dockercontainer.ListOptions) ([]dockertypes.Container, error)
	Inspect(ctx context.Context, containerID string) (dockertypes.ContainerJSON, error)
	Exists(ctx context.Context, containerID string) (bool, error)
}

// ── Result types for read-only operations ────────────────────────────────────

// ListResult carries the outcome of an async List call.
type ListResult struct {
	Containers []dockertypes.Container
	Err        error
}

// InspectResult carries the outcome of an async Inspect call.
type InspectResult struct {
	Info dockertypes.ContainerJSON
	Err  error
}

// ExistsResult carries the outcome of an async Exists call.
type ExistsResult struct {
	Present bool
	Err     error
}

// ── Ticket ────────────────────────────────────────────────────────────────────

// Ticket is returned immediately by every mutating operation.
// Done is closed once the underlying work has settled (success, failure, or
// abandonment).  The caller can use Ticket.Wait, select on Done directly, or
// ignore it entirely — state is updated regardless.
type Ticket struct {
	// ChangeID is the state.StateChange.ID created for this operation.
	// Use it with state.FindByID to inspect the outcome.
	ChangeID string

	// Done is closed when the operation has settled.
	Done <-chan struct{}
}

// Wait blocks until the operation settles or ctx expires.
func (t *Ticket) Wait(ctx context.Context) error {
	select {
	case <-t.Done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ── Internal work unit ────────────────────────────────────────────────────────

// workItem is the internal representation of one queued operation.
type workItem struct {
	change *dockercontainerstate.StateChange

	// ctx is the caller's context. If it is already done when the worker
	// picks up this item the change is abandoned rather than attempted.
	ctx context.Context

	// fn performs the actual ops call and returns the Docker container ID
	// (may be empty, e.g. for stops/removes/connect/disconnect) or an error.
	fn func(ctx context.Context) (dockerID string, err error)

	// done is closed by the worker when the item has settled.
	done chan struct{}
}

// ── Actor ─────────────────────────────────────────────────────────────────────

// Actor owns the async dispatch loop and is the single point of contact for
// callers that want to mutate or query container state.
type Actor struct {
	ops   opsProvider
	state *dockercontainerstate.State
	log   *logger.Logger
	cfg   Config

	queue     chan workItem
	closed    chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// New creates an Actor and immediately starts cfg.Workers background workers.
// Callers must call Close when finished.
func New(ops opsProvider, st *dockercontainerstate.State, log *logger.Logger, cfg *Config) *Actor {
	log.LowTrace("creating container actor")
	c := cfg.withDefaults()
	log.Debug("actor config: workers=%d queueSize=%d", c.Workers, c.QueueSize)

	a := &Actor{
		ops:    ops,
		state:  st,
		log:    log,
		cfg:    c,
		queue:  make(chan workItem, c.QueueSize),
		closed: make(chan struct{}),
	}

	log.Trace("starting %d worker goroutines", c.Workers)
	for i := range c.Workers {
		a.wg.Add(1)
		go a.runWorker(i)
	}

	log.Info("container actor ready (workers=%d queueSize=%d)", c.Workers, c.QueueSize)
	return a
}

// ── Mutating operations ───────────────────────────────────────────────────────

// Create submits an async container creation.  State is updated to active
// (with the daemon-reported container ID) on success, or failed on error.
// Pass nil networkConfig to skip network attachment at creation time.
func (a *Actor) Create(ctx context.Context, name string, config *dockercontainer.Config, hostConfig *dockercontainer.HostConfig, networkConfig *dockernetwork.NetworkingConfig) (*Ticket, error) {
	a.log.LowTrace("submitting create: name=%s image=%s", name, config.Image)

	meta := map[string]string{"image": config.Image, "name": name}
	change := a.state.RequestChange(dockercontainerstate.OpCreate, name, meta)
	a.log.Trace("create change registered: id=%s", change.ID)

	fn := func(ctx context.Context) (string, error) {
		a.log.Debug("worker: executing create for %q (change=%s)", name, change.ID)

		resp, err := a.ops.Create(ctx, name, config, hostConfig, networkConfig)
		if err != nil {
			a.log.Error("worker: create failed for %q: %v", name, err)
			return "", err
		}

		a.log.Debug("worker: create %q succeeded, containerID=%s", name, resp.ID)
		return resp.ID, nil
	}

	return a.submit(change, ctx, fn)
}

// Start submits an async container start.
func (a *Actor) Start(ctx context.Context, containerID string, opts dockercontainer.StartOptions) (*Ticket, error) {
	a.log.LowTrace("submitting start: %s", containerID)

	change := a.state.RequestChange(dockercontainerstate.OpStart, containerID, nil)
	a.log.Trace("start change registered: id=%s", change.ID)

	fn := func(ctx context.Context) (string, error) {
		a.log.Debug("worker: executing start for %q (change=%s)", containerID, change.ID)

		if err := a.ops.Start(ctx, containerID, opts); err != nil {
			a.log.Error("worker: start failed for %q: %v", containerID, err)
			return "", err
		}

		a.log.Debug("worker: start %q succeeded", containerID)
		return "", nil
	}

	return a.submit(change, ctx, fn)
}

// Stop submits an async container stop.
func (a *Actor) Stop(ctx context.Context, containerID string, opts dockercontainer.StopOptions) (*Ticket, error) {
	a.log.LowTrace("submitting stop: %s", containerID)

	change := a.state.RequestChange(dockercontainerstate.OpStop, containerID, nil)
	a.log.Trace("stop change registered: id=%s", change.ID)

	fn := func(ctx context.Context) (string, error) {
		a.log.Debug("worker: executing stop for %q (change=%s)", containerID, change.ID)

		if err := a.ops.Stop(ctx, containerID, opts); err != nil {
			a.log.Error("worker: stop failed for %q: %v", containerID, err)
			return "", err
		}

		a.log.Debug("worker: stop %q succeeded", containerID)
		return "", nil
	}

	return a.submit(change, ctx, fn)
}

// Remove submits an async container removal.
func (a *Actor) Remove(ctx context.Context, containerID string, opts dockercontainer.RemoveOptions) (*Ticket, error) {
	a.log.LowTrace("submitting remove: %s", containerID)

	meta := map[string]string{
		"force":         fmt.Sprintf("%v", opts.Force),
		"removeVolumes": fmt.Sprintf("%v", opts.RemoveVolumes),
	}
	change := a.state.RequestChange(dockercontainerstate.OpRemove, containerID, meta)
	a.log.Trace("remove change registered: id=%s", change.ID)

	fn := func(ctx context.Context) (string, error) {
		a.log.Debug("worker: executing remove for %q (change=%s)", containerID, change.ID)

		// Guard: block removal of the managed proxy container.
		if info, err := a.ops.Inspect(ctx, containerID); err == nil {
			if info.Config != nil {
				if info.Config.Labels["dokoko.portproxy"] == "true" {
					return "", errors.New("proxy container is managed by dokoko and cannot be removed")
				}
			}
		}
		// Inspect errors are non-fatal: let Docker return its own error on Remove.

		if err := a.ops.Remove(ctx, containerID, opts); err != nil {
			a.log.Error("worker: remove failed for %q: %v", containerID, err)
			return "", err
		}

		a.log.Debug("worker: remove %q succeeded", containerID)
		return "", nil
	}

	return a.submit(change, ctx, fn)
}

// Connect submits an async network connection for containerID to networkID.
// Pass nil config for default endpoint settings.
func (a *Actor) Connect(ctx context.Context, networkID, containerID string, config *dockernetwork.EndpointSettings) (*Ticket, error) {
	a.log.LowTrace("submitting connect: container=%s network=%s", containerID, networkID)

	meta := map[string]string{"networkID": networkID}
	change := a.state.RequestChange(dockercontainerstate.OpConnect, containerID, meta)
	a.log.Trace("connect change registered: id=%s", change.ID)

	fn := func(ctx context.Context) (string, error) {
		a.log.Debug("worker: executing connect container=%q network=%q (change=%s)", containerID, networkID, change.ID)

		if err := a.ops.Connect(ctx, networkID, containerID, config); err != nil {
			a.log.Error("worker: connect failed (container=%q network=%q): %v", containerID, networkID, err)
			return "", err
		}

		a.log.Debug("worker: connect %q → %q succeeded", containerID, networkID)
		return "", nil
	}

	return a.submit(change, ctx, fn)
}

// Disconnect submits an async network disconnection of containerID from networkID.
// Set force=true to disconnect even if the container is not running.
func (a *Actor) Disconnect(ctx context.Context, networkID, containerID string, force bool) (*Ticket, error) {
	a.log.LowTrace("submitting disconnect: container=%s network=%s", containerID, networkID)

	meta := map[string]string{
		"networkID": networkID,
		"force":     fmt.Sprintf("%v", force),
	}
	change := a.state.RequestChange(dockercontainerstate.OpDisconnect, containerID, meta)
	a.log.Trace("disconnect change registered: id=%s", change.ID)

	fn := func(ctx context.Context) (string, error) {
		a.log.Debug("worker: executing disconnect container=%q network=%q (change=%s)", containerID, networkID, change.ID)

		if err := a.ops.Disconnect(ctx, networkID, containerID, force); err != nil {
			a.log.Error("worker: disconnect failed (container=%q network=%q): %v", containerID, networkID, err)
			return "", err
		}

		a.log.Debug("worker: disconnect %q from %q succeeded", containerID, networkID)
		return "", nil
	}

	return a.submit(change, ctx, fn)
}

// ── Read-only operations ──────────────────────────────────────────────────────

// List asynchronously lists containers and sends the result on the returned
// buffered channel.  The caller may ignore the channel; the goroutine will
// not leak.
func (a *Actor) List(ctx context.Context, opts dockercontainer.ListOptions) <-chan ListResult {
	a.log.LowTrace("dispatching async list")
	ch := make(chan ListResult, 1)

	go func() {
		a.log.Trace("async list: calling ops.List")
		containers, err := a.ops.List(ctx, opts)
		if err != nil {
			a.log.Error("async list: ops.List failed: %v", err)
		} else {
			a.log.Debug("async list: returned %d containers", len(containers))
		}
		ch <- ListResult{Containers: containers, Err: err}
	}()

	return ch
}

// Inspect asynchronously inspects containerID and sends the result on the
// returned buffered channel.
func (a *Actor) Inspect(ctx context.Context, containerID string) <-chan InspectResult {
	a.log.LowTrace("dispatching async inspect: %s", containerID)
	ch := make(chan InspectResult, 1)

	go func() {
		a.log.Trace("async inspect: calling ops.Inspect for %q", containerID)
		info, err := a.ops.Inspect(ctx, containerID)
		if err != nil {
			a.log.Error("async inspect: ops.Inspect failed for %q: %v", containerID, err)
		} else {
			a.log.Debug("async inspect: %q → id=%s running=%v", containerID, info.ID, info.State.Running)
		}
		ch <- InspectResult{Info: info, Err: err}
	}()

	return ch
}

// Exists asynchronously checks whether containerID is present.
func (a *Actor) Exists(ctx context.Context, containerID string) <-chan ExistsResult {
	a.log.LowTrace("dispatching async exists: %s", containerID)
	ch := make(chan ExistsResult, 1)

	go func() {
		a.log.Trace("async exists: calling ops.Exists for %q", containerID)
		present, err := a.ops.Exists(ctx, containerID)
		if err != nil {
			a.log.Error("async exists: ops.Exists failed for %q: %v", containerID, err)
		} else {
			a.log.Debug("async exists: %q present=%v", containerID, present)
		}
		ch <- ExistsResult{Present: present, Err: err}
	}()

	return ch
}

// ── Shutdown ──────────────────────────────────────────────────────────────────

// Close stops the actor gracefully.  In-flight work already picked up by
// workers runs to completion; items still sitting in the queue are abandoned.
// Close is safe to call multiple times; subsequent calls are no-ops.
func (a *Actor) Close() {
	a.closeOnce.Do(func() {
		a.log.LowTrace("closing container actor")
		a.log.Debug("signalling workers to stop and draining queue")

		// Signal workers to stop accepting new items.
		close(a.closed)

		// Wait for all workers to finish their current item and drain the queue.
		a.wg.Wait()

		a.log.Info("container actor closed, all workers exited")
	})
}

// ── Internal helpers ──────────────────────────────────────────────────────────

// submit registers a work item and enqueues it non-blockingly.
// If the actor is closed or the queue is full, the change is abandoned.
func (a *Actor) submit(change *dockercontainerstate.StateChange, ctx context.Context, fn func(context.Context) (string, error)) (*Ticket, error) {
	a.log.Trace("submit: change=%s ref=%s", change.ID, change.ContainerRef)

	// Fast-path: actor already closed.
	select {
	case <-a.closed:
		a.log.Warn("submit: actor closed, abandoning change %s", change.ID)
		_, _ = a.state.Abandon(change.ID, "actor closed")
		return nil, ErrActorClosed
	default:
	}

	done := make(chan struct{})
	item := workItem{change: change, ctx: ctx, fn: fn, done: done}

	select {
	case a.queue <- item:
		a.log.Debug("submit: change %s enqueued (queueLen≈%d)", change.ID, len(a.queue))
		return &Ticket{ChangeID: change.ID, Done: done}, nil

	case <-a.closed:
		// Closed between the fast-path check and the send.
		a.log.Warn("submit: actor closed mid-submit, abandoning change %s", change.ID)
		_, _ = a.state.Abandon(change.ID, "actor closed")
		close(done)
		return nil, ErrActorClosed

	default:
		// Queue is full.
		a.log.Warn("submit: queue full, abandoning change %s (op=%s ref=%s)",
			change.ID, change.Op, change.ContainerRef)
		_, _ = a.state.Abandon(change.ID, "work queue full")
		close(done)
		return nil, ErrQueueFull
	}
}

// runWorker is the main loop for one worker goroutine.
func (a *Actor) runWorker(workerID int) {
	defer a.wg.Done()
	a.log.Debug("worker %d started", workerID)

	for {
		select {
		case item, ok := <-a.queue:
			if !ok {
				// Queue channel closed — should not happen in normal flow, but
				// handle defensively.
				a.log.Debug("worker %d: queue channel closed, exiting", workerID)
				return
			}
			a.log.Trace("worker %d: picked up change %s (op=%s ref=%s)",
				workerID, item.change.ID, item.change.Op, item.change.ContainerRef)
			a.execute(workerID, item)

		case <-a.closed:
			a.log.Debug("worker %d: closed signal received, draining queue", workerID)
			a.drainQueue(workerID)
			a.log.Debug("worker %d: exiting", workerID)
			return
		}
	}
}

// drainQueue abandons every item still in the queue after the closed signal.
func (a *Actor) drainQueue(workerID int) {
	for {
		select {
		case item := <-a.queue:
			a.log.Debug("worker %d: draining change %s — abandoning", workerID, item.change.ID)
			_, _ = a.state.Abandon(item.change.ID, "actor shutting down")
			close(item.done)
		default:
			return
		}
	}
}

// execute runs one work item, updates state, and closes item.done.
func (a *Actor) execute(workerID int, item workItem) {
	defer close(item.done)

	changeID := item.change.ID
	a.log.Debug("worker %d: executing change %s (op=%s ref=%s)",
		workerID, changeID, item.change.Op, item.change.ContainerRef)

	// If the caller's context is already done, abandon without calling ops.
	select {
	case <-item.ctx.Done():
		reason := fmt.Sprintf("context done before execution: %v", item.ctx.Err())
		a.log.Warn("worker %d: %s (change=%s)", workerID, reason, changeID)
		_, _ = a.state.Abandon(changeID, reason)
		return
	default:
	}

	// Run the operation.
	dockerID, err := item.fn(item.ctx)
	if err != nil {
		a.log.Error("worker %d: change %s failed: %v", workerID, changeID, err)
		_, _ = a.state.RecordFailure(changeID, err)
		return
	}

	a.log.Debug("worker %d: change %s succeeded (dockerID=%q)", workerID, changeID, dockerID)
	_, _ = a.state.ConfirmSuccess(changeID, dockerID)
	a.log.Info("worker %d: change %s settled → active", workerID, changeID)
}
