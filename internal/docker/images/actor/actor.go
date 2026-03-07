// Package dockerimageactor dispatches Docker image operations asynchronously,
// keeping dockerimagestate in sync with every outcome.
//
// Flow for mutating operations (Pull, Remove, Tag):
//
//	caller → Actor.Pull()
//	            → state.RequestChange()       [immediate, on caller goroutine]
//	            → enqueue work item           [immediate, non-blocking]
//	            ← *Ticket{ChangeID, Done}     [returned to caller immediately]
//
//	            … worker goroutine picks up work …
//	            → ops.Pull()
//	            → state.ConfirmSuccess()  (on success)
//	            → state.RecordFailure()   (on ops error)
//	            → state.Abandon()         (if context was cancelled before execution)
//	            → close(Ticket.Done)
//
// Read-only operations (List, Inspect, Exists) bypass state and fire a single
// goroutine each, returning a buffered result channel so the caller is never
// forced to block.
package dockerimageactor

import (
	"context"
	"errors"
	"fmt"
	"sync"

	dockerimagestate "dokoko.ai/dokoko/internal/docker/images/state"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
	dockerimage "github.com/docker/docker/api/types/image"
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

// opsProvider is the subset of *dockerimageops.Ops that the Actor uses.
// Declared here so tests can inject a fake without a real Docker daemon.
type opsProvider interface {
	Pull(ctx context.Context, ref string, opts dockerimage.PullOptions) error
	List(ctx context.Context, opts dockerimage.ListOptions) ([]dockerimage.Summary, error)
	Inspect(ctx context.Context, imageID string) (dockertypes.ImageInspect, error)
	Remove(ctx context.Context, imageID string, opts dockerimage.RemoveOptions) ([]dockerimage.DeleteResponse, error)
	Tag(ctx context.Context, source, target string) error
	Exists(ctx context.Context, ref string) (bool, error)
}

// ── Result types for read-only operations ────────────────────────────────────

// ListResult carries the outcome of an async List call.
type ListResult struct {
	Images []dockerimage.Summary
	Err    error
}

// InspectResult carries the outcome of an async Inspect call.
type InspectResult struct {
	Info dockertypes.ImageInspect
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
	change *dockerimagestate.StateChange

	// ctx is the caller's context. If it is already done when the worker
	// picks up this item the change is abandoned rather than attempted.
	ctx context.Context

	// fn performs the actual ops call and returns the Docker image ID (may be
	// empty, e.g. for removes) or an error.
	fn func(ctx context.Context) (dockerID string, err error)

	// done is closed by the worker when the item has settled.
	done chan struct{}
}

// ── Actor ─────────────────────────────────────────────────────────────────────

// Actor owns the async dispatch loop and is the single point of contact for
// callers that want to mutate or query image state.
type Actor struct {
	ops   opsProvider
	state *dockerimagestate.State
	log   *logger.Logger
	cfg   Config

	queue     chan workItem
	closed    chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// New creates an Actor and immediately starts cfg.Workers background workers.
// Callers must call Close when finished.
func New(ops opsProvider, st *dockerimagestate.State, log *logger.Logger, cfg *Config) *Actor {
	log.LowTrace("creating actor")
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

	log.Info("actor ready (workers=%d queueSize=%d)", c.Workers, c.QueueSize)
	return a
}

// ── Mutating operations ───────────────────────────────────────────────────────

// Pull submits an async pull for ref.  State is updated to active (with the
// daemon-reported image ID) on success, or failed on error.
func (a *Actor) Pull(ctx context.Context, ref string, opts dockerimage.PullOptions) (*Ticket, error) {
	a.log.LowTrace("submitting pull: %s", ref)
	a.log.Debug("pull opts: platform=%q auth=%v", opts.Platform, opts.RegistryAuth != "")

	meta := map[string]string{"platform": opts.Platform}
	change := a.state.RequestChange(dockerimagestate.OpPull, ref, meta)
	a.log.Trace("pull change registered: id=%s", change.ID)

	fn := func(ctx context.Context) (string, error) {
		a.log.Debug("worker: executing pull for %q (change=%s)", ref, change.ID)

		if err := a.ops.Pull(ctx, ref, opts); err != nil {
			a.log.Error("worker: pull failed for %q: %v", ref, err)
			return "", err
		}

		a.log.Trace("worker: pull succeeded for %q, inspecting for image ID", ref)
		info, err := a.ops.Inspect(ctx, ref)
		if err != nil {
			a.log.Warn("worker: pull %q ok but inspect failed (dockerID will be empty): %v", ref, err)
			return "", nil
		}

		a.log.Debug("worker: pull %q resolved to dockerID=%s", ref, info.ID)
		return info.ID, nil
	}

	return a.submit(change, ctx, fn)
}

// Remove submits an async removal of imageID.
func (a *Actor) Remove(ctx context.Context, imageID string, opts dockerimage.RemoveOptions) (*Ticket, error) {
	a.log.LowTrace("submitting remove: %s", imageID)
	a.log.Debug("remove opts: force=%v pruneChildren=%v", opts.Force, opts.PruneChildren)

	meta := map[string]string{
		"force":         fmt.Sprintf("%v", opts.Force),
		"pruneChildren": fmt.Sprintf("%v", opts.PruneChildren),
	}
	change := a.state.RequestChange(dockerimagestate.OpRemove, imageID, meta)
	a.log.Trace("remove change registered: id=%s", change.ID)

	fn := func(ctx context.Context) (string, error) {
		a.log.Debug("worker: executing remove for %q (change=%s)", imageID, change.ID)

		responses, err := a.ops.Remove(ctx, imageID, opts)
		if err != nil {
			a.log.Error("worker: remove failed for %q: %v", imageID, err)
			return "", err
		}

		a.log.Debug("worker: remove %q succeeded (%d entries)", imageID, len(responses))
		return "", nil // removes don't yield a new image ID
	}

	return a.submit(change, ctx, fn)
}

// Tag submits an async tag of source → target.
func (a *Actor) Tag(ctx context.Context, source, target string) (*Ticket, error) {
	a.log.LowTrace("submitting tag: %s → %s", source, target)

	meta := map[string]string{"source": source, "target": target}
	change := a.state.RequestChange(dockerimagestate.OpTag, source, meta)
	a.log.Trace("tag change registered: id=%s", change.ID)

	fn := func(ctx context.Context) (string, error) {
		a.log.Debug("worker: executing tag %q → %q (change=%s)", source, target, change.ID)

		if err := a.ops.Tag(ctx, source, target); err != nil {
			a.log.Error("worker: tag failed (%q → %q): %v", source, target, err)
			return "", err
		}

		a.log.Debug("worker: tag %q → %q succeeded", source, target)
		return "", nil
	}

	return a.submit(change, ctx, fn)
}

// ── Read-only operations ──────────────────────────────────────────────────────

// List asynchronously lists images and sends the result on the returned
// buffered channel.  The caller may ignore the channel; the goroutine will
// not leak.
func (a *Actor) List(ctx context.Context, opts dockerimage.ListOptions) <-chan ListResult {
	a.log.LowTrace("dispatching async list")
	ch := make(chan ListResult, 1)

	go func() {
		a.log.Trace("async list: calling ops.List")
		images, err := a.ops.List(ctx, opts)
		if err != nil {
			a.log.Error("async list: ops.List failed: %v", err)
		} else {
			a.log.Debug("async list: returned %d images", len(images))
		}
		ch <- ListResult{Images: images, Err: err}
	}()

	return ch
}

// Inspect asynchronously inspects imageID and sends the result on the returned
// buffered channel.
func (a *Actor) Inspect(ctx context.Context, imageID string) <-chan InspectResult {
	a.log.LowTrace("dispatching async inspect: %s", imageID)
	ch := make(chan InspectResult, 1)

	go func() {
		a.log.Trace("async inspect: calling ops.Inspect for %q", imageID)
		info, err := a.ops.Inspect(ctx, imageID)
		if err != nil {
			a.log.Error("async inspect: ops.Inspect failed for %q: %v", imageID, err)
		} else {
			a.log.Debug("async inspect: %q → id=%s os=%s", imageID, info.ID, info.Os)
		}
		ch <- InspectResult{Info: info, Err: err}
	}()

	return ch
}

// Exists asynchronously checks whether ref is present in the local image store.
func (a *Actor) Exists(ctx context.Context, ref string) <-chan ExistsResult {
	a.log.LowTrace("dispatching async exists: %s", ref)
	ch := make(chan ExistsResult, 1)

	go func() {
		a.log.Trace("async exists: calling ops.Exists for %q", ref)
		present, err := a.ops.Exists(ctx, ref)
		if err != nil {
			a.log.Error("async exists: ops.Exists failed for %q: %v", ref, err)
		} else {
			a.log.Debug("async exists: %q present=%v", ref, present)
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
		a.log.LowTrace("closing actor")
		a.log.Debug("signalling workers to stop and draining queue")

		// Signal workers to stop accepting new items.
		close(a.closed)

		// Wait for all workers to finish their current item and drain the queue.
		a.wg.Wait()

		a.log.Info("actor closed, all workers exited")
	})
}

// ── Internal helpers ──────────────────────────────────────────────────────────

// submit registers a work item and enqueues it non-blockingly.
// If the actor is closed or the queue is full, the change is abandoned.
func (a *Actor) submit(change *dockerimagestate.StateChange, ctx context.Context, fn func(context.Context) (string, error)) (*Ticket, error) {
	a.log.Trace("submit: change=%s ref=%s", change.ID, change.ImageRef)

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
			change.ID, change.Op, change.ImageRef)
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
				workerID, item.change.ID, item.change.Op, item.change.ImageRef)
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
		workerID, changeID, item.change.Op, item.change.ImageRef)

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
