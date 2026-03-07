// Package dockercontainerexecactor dispatches Docker exec operations
// asynchronously, keeping dockercontainerexecstate in sync with every outcome.
//
// Flow for mutating operations (Create, Start, Resize):
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
// Read-only operations (Inspect, Attach) bypass state and fire a single
// goroutine each, returning a buffered result channel so the caller is never
// forced to block.
package dockercontainerexecactor

import (
	"context"
	"errors"
	"fmt"
	"sync"

	dockercontainerexecstate "dokoko.ai/dokoko/internal/docker/containerexec/state"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"
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

// opsProvider is the subset of *dockercontainerexecops.Ops that the Actor uses.
// Declared here so tests can inject a fake without a real Docker daemon.
type opsProvider interface {
	Create(ctx context.Context, containerID string, config dockertypes.ExecConfig) (dockertypes.IDResponse, error)
	Start(ctx context.Context, execID string, config dockertypes.ExecStartCheck) error
	Attach(ctx context.Context, execID string, config dockertypes.ExecStartCheck) (dockertypes.HijackedResponse, error)
	Inspect(ctx context.Context, execID string) (dockertypes.ContainerExecInspect, error)
	Resize(ctx context.Context, execID string, opts dockercontainer.ResizeOptions) error
}

// ── Result types for read-only operations ────────────────────────────────────

// InspectResult carries the outcome of an async Inspect call.
type InspectResult struct {
	Info dockertypes.ContainerExecInspect
	Err  error
}

// AttachResult carries the outcome of an async Attach call.
// The caller owns Hijack and must call Close on it when finished.
type AttachResult struct {
	Hijack dockertypes.HijackedResponse
	Err    error
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

type workItem struct {
	change *dockercontainerexecstate.StateChange

	// ctx is the caller's context. If it is already done when the worker
	// picks up this item the change is abandoned rather than attempted.
	ctx context.Context

	// fn performs the actual ops call and returns the exec ID
	// (non-empty only for create; empty for start/resize) or an error.
	fn func(ctx context.Context) (execID string, err error)

	// done is closed by the worker when the item has settled.
	done chan struct{}
}

// ── Actor ─────────────────────────────────────────────────────────────────────

// Actor owns the async dispatch loop and is the single point of contact for
// callers that want to mutate or query exec state.
type Actor struct {
	ops   opsProvider
	state *dockercontainerexecstate.State
	log   *logger.Logger
	cfg   Config

	queue     chan workItem
	closed    chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// New creates an Actor and immediately starts cfg.Workers background workers.
// Callers must call Close when finished.
func New(ops opsProvider, st *dockercontainerexecstate.State, log *logger.Logger, cfg *Config) *Actor {
	log.LowTrace("creating exec actor")
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

	log.Info("exec actor ready (workers=%d queueSize=%d)", c.Workers, c.QueueSize)
	return a
}

// ── Mutating operations ───────────────────────────────────────────────────────

// Create submits an async exec instance creation inside containerID.
// State is updated to active (with the daemon-reported exec ID) on success,
// or failed on error.
func (a *Actor) Create(ctx context.Context, containerID string, config dockertypes.ExecConfig) (*Ticket, error) {
	a.log.LowTrace("submitting exec create: container=%s", containerID)

	meta := map[string]string{
		"containerID": containerID,
		"cmd":         fmt.Sprintf("%v", config.Cmd),
		"tty":         fmt.Sprintf("%v", config.Tty),
	}
	change := a.state.RequestChange(dockercontainerexecstate.OpExecCreate, containerID, meta)
	a.log.Trace("exec create change registered: id=%s", change.ID)

	fn := func(ctx context.Context) (string, error) {
		a.log.Debug("worker: executing exec create for container %q (change=%s)", containerID, change.ID)

		resp, err := a.ops.Create(ctx, containerID, config)
		if err != nil {
			a.log.Error("worker: exec create failed for container %q: %v", containerID, err)
			return "", err
		}

		a.log.Debug("worker: exec create succeeded, execID=%s", resp.ID)
		return resp.ID, nil
	}

	return a.submit(change, ctx, fn)
}

// Start submits an async exec start for the instance identified by execID.
func (a *Actor) Start(ctx context.Context, execID string, config dockertypes.ExecStartCheck) (*Ticket, error) {
	a.log.LowTrace("submitting exec start: %s", execID)

	meta := map[string]string{
		"detach": fmt.Sprintf("%v", config.Detach),
		"tty":    fmt.Sprintf("%v", config.Tty),
	}
	change := a.state.RequestChange(dockercontainerexecstate.OpExecStart, execID, meta)
	a.log.Trace("exec start change registered: id=%s", change.ID)

	fn := func(ctx context.Context) (string, error) {
		a.log.Debug("worker: executing exec start for %q (change=%s)", execID, change.ID)

		if err := a.ops.Start(ctx, execID, config); err != nil {
			a.log.Error("worker: exec start failed for %q: %v", execID, err)
			return "", err
		}

		a.log.Debug("worker: exec start %q succeeded", execID)
		return "", nil
	}

	return a.submit(change, ctx, fn)
}

// Resize submits an async TTY resize for the exec instance identified by execID.
func (a *Actor) Resize(ctx context.Context, execID string, opts dockercontainer.ResizeOptions) (*Ticket, error) {
	a.log.LowTrace("submitting exec resize: %s", execID)

	meta := map[string]string{
		"height": fmt.Sprintf("%d", opts.Height),
		"width":  fmt.Sprintf("%d", opts.Width),
	}
	change := a.state.RequestChange(dockercontainerexecstate.OpExecResize, execID, meta)
	a.log.Trace("exec resize change registered: id=%s", change.ID)

	fn := func(ctx context.Context) (string, error) {
		a.log.Debug("worker: executing exec resize for %q (change=%s)", execID, change.ID)

		if err := a.ops.Resize(ctx, execID, opts); err != nil {
			a.log.Error("worker: exec resize failed for %q: %v", execID, err)
			return "", err
		}

		a.log.Debug("worker: exec resize %q succeeded", execID)
		return "", nil
	}

	return a.submit(change, ctx, fn)
}

// ExecDockerID returns the Docker-assigned exec ID for a settled create change.
// Call this after the create Ticket.Done is closed.
// Returns an error if the change is not found or did not succeed.
func (a *Actor) ExecDockerID(changeID string) (string, error) {
	status, record, err := a.state.FindByID(changeID)
	if err != nil {
		return "", err
	}
	if status != dockercontainerexecstate.StatusActive {
		return "", fmt.Errorf("exec change %s did not succeed (status=%s)", changeID, status)
	}
	rec := record.(*dockercontainerexecstate.ActiveRecord)
	return rec.ExecID, nil
}

// ── Read-only operations ──────────────────────────────────────────────────────

// Inspect asynchronously inspects the exec instance identified by execID and
// sends the result on the returned buffered channel.
func (a *Actor) Inspect(ctx context.Context, execID string) <-chan InspectResult {
	a.log.LowTrace("dispatching async exec inspect: %s", execID)
	ch := make(chan InspectResult, 1)

	go func() {
		a.log.Trace("async inspect: calling ops.Inspect for %q", execID)
		info, err := a.ops.Inspect(ctx, execID)
		if err != nil {
			a.log.Error("async inspect: ops.Inspect failed for %q: %v", execID, err)
		} else {
			a.log.Debug("async inspect: %q running=%v exitCode=%d", execID, info.Running, info.ExitCode)
		}
		ch <- InspectResult{Info: info, Err: err}
	}()

	return ch
}

// Attach asynchronously attaches to the exec instance identified by execID
// for interactive I/O and sends the result on the returned buffered channel.
// The caller owns the HijackedResponse in AttachResult and must Close it.
func (a *Actor) Attach(ctx context.Context, execID string, config dockertypes.ExecStartCheck) <-chan AttachResult {
	a.log.LowTrace("dispatching async exec attach: %s", execID)
	ch := make(chan AttachResult, 1)

	go func() {
		a.log.Trace("async attach: calling ops.Attach for %q", execID)
		hijack, err := a.ops.Attach(ctx, execID, config)
		if err != nil {
			a.log.Error("async attach: ops.Attach failed for %q: %v", execID, err)
		} else {
			a.log.Debug("async attach: connection established for %q", execID)
		}
		ch <- AttachResult{Hijack: hijack, Err: err}
	}()

	return ch
}

// ── Shutdown ──────────────────────────────────────────────────────────────────

// Close stops the actor gracefully.  In-flight work already picked up by
// workers runs to completion; items still sitting in the queue are abandoned.
// Close is safe to call multiple times; subsequent calls are no-ops.
func (a *Actor) Close() {
	a.closeOnce.Do(func() {
		a.log.LowTrace("closing exec actor")
		a.log.Debug("signalling workers to stop and draining queue")

		close(a.closed)
		a.wg.Wait()

		a.log.Info("exec actor closed, all workers exited")
	})
}

// ── Internal helpers ──────────────────────────────────────────────────────────

func (a *Actor) submit(change *dockercontainerexecstate.StateChange, ctx context.Context, fn func(context.Context) (string, error)) (*Ticket, error) {
	a.log.Trace("submit: change=%s ref=%s", change.ID, change.ExecRef)

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
		a.log.Warn("submit: actor closed mid-submit, abandoning change %s", change.ID)
		_, _ = a.state.Abandon(change.ID, "actor closed")
		close(done)
		return nil, ErrActorClosed

	default:
		a.log.Warn("submit: queue full, abandoning change %s (op=%s ref=%s)",
			change.ID, change.Op, change.ExecRef)
		_, _ = a.state.Abandon(change.ID, "work queue full")
		close(done)
		return nil, ErrQueueFull
	}
}

func (a *Actor) runWorker(workerID int) {
	defer a.wg.Done()
	a.log.Debug("worker %d started", workerID)

	for {
		select {
		case item, ok := <-a.queue:
			if !ok {
				a.log.Debug("worker %d: queue channel closed, exiting", workerID)
				return
			}
			a.log.Trace("worker %d: picked up change %s (op=%s ref=%s)",
				workerID, item.change.ID, item.change.Op, item.change.ExecRef)
			a.execute(workerID, item)

		case <-a.closed:
			a.log.Debug("worker %d: closed signal received, draining queue", workerID)
			a.drainQueue(workerID)
			a.log.Debug("worker %d: exiting", workerID)
			return
		}
	}
}

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

func (a *Actor) execute(workerID int, item workItem) {
	defer close(item.done)

	changeID := item.change.ID
	a.log.Debug("worker %d: executing change %s (op=%s ref=%s)",
		workerID, changeID, item.change.Op, item.change.ExecRef)

	select {
	case <-item.ctx.Done():
		reason := fmt.Sprintf("context done before execution: %v", item.ctx.Err())
		a.log.Warn("worker %d: %s (change=%s)", workerID, reason, changeID)
		_, _ = a.state.Abandon(changeID, reason)
		return
	default:
	}

	execID, err := item.fn(item.ctx)
	if err != nil {
		a.log.Error("worker %d: change %s failed: %v", workerID, changeID, err)
		_, _ = a.state.RecordFailure(changeID, err)
		return
	}

	a.log.Debug("worker %d: change %s succeeded (execID=%q)", workerID, changeID, execID)
	_, _ = a.state.ConfirmSuccess(changeID, execID)
	a.log.Info("worker %d: change %s settled → active", workerID, changeID)
}
