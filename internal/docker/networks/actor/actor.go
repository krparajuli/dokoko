// Package dockernetworkactor dispatches Docker network operations asynchronously,
// keeping dockernetworkstate in sync with every outcome.
//
// Flow for mutating operations (Create, Remove, Prune):
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
// Read-only operations (List, Inspect) bypass state and fire a single
// goroutine each, returning a buffered result channel so the caller is never
// forced to block.
package dockernetworkactor

import (
	"context"
	"errors"
	"fmt"
	"sync"

	dockernetworkstate "dokoko.ai/dokoko/internal/docker/networks/state"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
	dockerfilters "github.com/docker/docker/api/types/filters"
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

// opsProvider is the subset of *dockernetworkops.Ops that the Actor uses.
// Declared here so tests can inject a fake without a real Docker daemon.
type opsProvider interface {
	Create(ctx context.Context, name string, opts dockertypes.NetworkCreate) (dockertypes.NetworkCreateResponse, error)
	List(ctx context.Context, opts dockertypes.NetworkListOptions) ([]dockertypes.NetworkResource, error)
	Inspect(ctx context.Context, networkID string, opts dockertypes.NetworkInspectOptions) (dockertypes.NetworkResource, error)
	Remove(ctx context.Context, networkID string) error
	Prune(ctx context.Context, pruneFilter dockerfilters.Args) (dockertypes.NetworksPruneReport, error)
}

// ── Result types for read-only operations ─────────────────────────────────────

// ListResult carries the outcome of an async List call.
type ListResult struct {
	Networks []dockertypes.NetworkResource
	Err      error
}

// InspectResult carries the outcome of an async Inspect call.
type InspectResult struct {
	Network dockertypes.NetworkResource
	Err     error
}

// ── Ticket ────────────────────────────────────────────────────────────────────

// Ticket is returned immediately by every mutating operation.
// Done is closed once the underlying work has settled (success, failure, or
// abandonment).  The caller can use Ticket.Wait, select on Done directly, or
// ignore it entirely — state is updated regardless.
type Ticket struct {
	// ChangeID is the state.StateChange.ID created for this operation.
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
	change *dockernetworkstate.StateChange
	ctx    context.Context
	fn     func(ctx context.Context) (networkID string, err error)
	done   chan struct{}
}

// ── Actor ─────────────────────────────────────────────────────────────────────

// Actor owns the async dispatch loop and is the single point of contact for
// callers that want to mutate or query network state.
type Actor struct {
	ops   opsProvider
	state *dockernetworkstate.State
	log   *logger.Logger
	cfg   Config

	queue     chan workItem
	closed    chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// New creates an Actor and immediately starts cfg.Workers background workers.
// Callers must call Close when finished.
func New(ops opsProvider, st *dockernetworkstate.State, log *logger.Logger, cfg *Config) *Actor {
	log.LowTrace("creating network actor")
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

	log.Info("network actor ready (workers=%d queueSize=%d)", c.Workers, c.QueueSize)
	return a
}

// ── Mutating operations ───────────────────────────────────────────────────────

// Create submits an async network creation.  State is updated to active (with
// the daemon-assigned ID) on success, or failed on error.
func (a *Actor) Create(ctx context.Context, name string, opts dockertypes.NetworkCreate) (*Ticket, error) {
	a.log.LowTrace("submitting create: name=%q driver=%q", name, opts.Driver)

	meta := map[string]string{
		"name":   name,
		"driver": opts.Driver,
	}
	change := a.state.RequestChange(dockernetworkstate.OpCreate, name, meta)
	a.log.Trace("create change registered: id=%s", change.ID)

	fn := func(ctx context.Context) (string, error) {
		a.log.Debug("worker: executing create for %q (change=%s)", name, change.ID)

		resp, err := a.ops.Create(ctx, name, opts)
		if err != nil {
			a.log.Error("worker: create failed for %q: %v", name, err)
			return "", err
		}

		a.log.Debug("worker: create %q succeeded → networkID=%s", name, resp.ID)
		return resp.ID, nil
	}

	return a.submit(change, ctx, fn)
}

// Remove submits an async network removal.
func (a *Actor) Remove(ctx context.Context, networkID string) (*Ticket, error) {
	a.log.LowTrace("submitting remove: networkID=%q", networkID)

	change := a.state.RequestChange(dockernetworkstate.OpRemove, networkID, nil)
	a.log.Trace("remove change registered: id=%s", change.ID)

	fn := func(ctx context.Context) (string, error) {
		a.log.Debug("worker: executing remove for %q (change=%s)", networkID, change.ID)

		if err := a.ops.Remove(ctx, networkID); err != nil {
			a.log.Error("worker: remove failed for %q: %v", networkID, err)
			return "", err
		}

		a.log.Debug("worker: remove %q succeeded", networkID)
		return "", nil
	}

	return a.submit(change, ctx, fn)
}

// Prune submits an async prune of unused networks.
func (a *Actor) Prune(ctx context.Context, pruneFilter dockerfilters.Args) (*Ticket, error) {
	a.log.LowTrace("submitting prune")

	meta := map[string]string{"filters": fmt.Sprintf("%v", pruneFilter)}
	change := a.state.RequestChange(dockernetworkstate.OpPrune, "", meta)
	a.log.Trace("prune change registered: id=%s", change.ID)

	fn := func(ctx context.Context) (string, error) {
		a.log.Debug("worker: executing prune (change=%s)", change.ID)

		report, err := a.ops.Prune(ctx, pruneFilter)
		if err != nil {
			a.log.Error("worker: prune failed: %v", err)
			return "", err
		}

		a.log.Debug("worker: prune succeeded: removed=%v", report.NetworksDeleted)
		return "", nil
	}

	return a.submit(change, ctx, fn)
}

// ── Read-only operations ──────────────────────────────────────────────────────

// List asynchronously lists networks and sends the result on the returned
// buffered channel.  The caller may ignore the channel; the goroutine will
// not leak.
func (a *Actor) List(ctx context.Context, opts dockertypes.NetworkListOptions) <-chan ListResult {
	a.log.LowTrace("dispatching async list")
	ch := make(chan ListResult, 1)

	go func() {
		a.log.Trace("async list: calling ops.List")
		networks, err := a.ops.List(ctx, opts)
		if err != nil {
			a.log.Error("async list: ops.List failed: %v", err)
		} else {
			a.log.Debug("async list: returned %d networks", len(networks))
		}
		ch <- ListResult{Networks: networks, Err: err}
	}()

	return ch
}

// Inspect asynchronously inspects the named network and sends the result on
// the returned buffered channel.
func (a *Actor) Inspect(ctx context.Context, networkID string, opts dockertypes.NetworkInspectOptions) <-chan InspectResult {
	a.log.LowTrace("dispatching async inspect: %s", networkID)
	ch := make(chan InspectResult, 1)

	go func() {
		a.log.Trace("async inspect: calling ops.Inspect for %q", networkID)
		net, err := a.ops.Inspect(ctx, networkID, opts)
		if err != nil {
			a.log.Error("async inspect: ops.Inspect failed for %q: %v", networkID, err)
		} else {
			a.log.Debug("async inspect: %q name=%s driver=%s", networkID, net.Name, net.Driver)
		}
		ch <- InspectResult{Network: net, Err: err}
	}()

	return ch
}

// ── Shutdown ──────────────────────────────────────────────────────────────────

// Close stops the actor gracefully.  In-flight work already picked up by
// workers runs to completion; items still sitting in the queue are abandoned.
// Close is safe to call multiple times; subsequent calls are no-ops.
func (a *Actor) Close() {
	a.closeOnce.Do(func() {
		a.log.LowTrace("closing network actor")
		a.log.Debug("signalling workers to stop and draining queue")
		close(a.closed)
		a.wg.Wait()
		a.log.Info("network actor closed, all workers exited")
	})
}

// ── Internal helpers ──────────────────────────────────────────────────────────

func (a *Actor) submit(change *dockernetworkstate.StateChange, ctx context.Context, fn func(context.Context) (string, error)) (*Ticket, error) {
	a.log.Trace("submit: change=%s name=%s", change.ID, change.NetworkName)

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
		a.log.Warn("submit: queue full, abandoning change %s (op=%s name=%s)",
			change.ID, change.Op, change.NetworkName)
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
			a.log.Trace("worker %d: picked up change %s (op=%s name=%s)",
				workerID, item.change.ID, item.change.Op, item.change.NetworkName)
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
	a.log.Debug("worker %d: executing change %s (op=%s name=%s)",
		workerID, changeID, item.change.Op, item.change.NetworkName)

	select {
	case <-item.ctx.Done():
		reason := fmt.Sprintf("context done before execution: %v", item.ctx.Err())
		a.log.Warn("worker %d: %s (change=%s)", workerID, reason, changeID)
		_, _ = a.state.Abandon(changeID, reason)
		return
	default:
	}

	networkID, err := item.fn(item.ctx)
	if err != nil {
		a.log.Error("worker %d: change %s failed: %v", workerID, changeID, err)
		_, _ = a.state.RecordFailure(changeID, err)
		return
	}

	a.log.Debug("worker %d: change %s succeeded (networkID=%q)", workerID, changeID, networkID)
	_, _ = a.state.ConfirmSuccess(changeID, networkID)
	a.log.Info("worker %d: change %s settled → active", workerID, changeID)
}
