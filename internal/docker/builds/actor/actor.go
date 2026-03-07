// Package dockerbuildactor dispatches Docker image build operations
// asynchronously, keeping dockerbuildstate in sync with every outcome.
//
// Flow for mutating operations (Build, PruneCache):
//
//	caller → Actor.Build()
//	            → state.RequestChange()       [immediate, on caller goroutine]
//	            → enqueue work item           [immediate, non-blocking]
//	            ← *Ticket{ChangeID, Done}     [returned to caller immediately]
//
//	            … worker goroutine picks up work …
//	            → ops.Build()
//	            → state.ConfirmSuccess()  (on success)
//	            → state.RecordFailure()   (on ops error)
//	            → state.Abandon()         (if context was cancelled before execution)
//	            → close(Ticket.Done)
package dockerbuildactor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	dockerbuildops "dokoko.ai/dokoko/internal/docker/builds/ops"
	dockerbuildstate "dokoko.ai/dokoko/internal/docker/builds/state"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
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

// opsProvider is the subset of *dockerbuildops.Ops that the Actor uses.
// Declared here so tests can inject a fake without a real Docker daemon.
type opsProvider interface {
	Build(ctx context.Context, req dockerbuildops.BuildRequest) (dockerbuildops.BuildResponse, error)
	PruneCache(ctx context.Context, opts dockertypes.BuildCachePruneOptions) (*dockertypes.BuildCachePruneReport, error)
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
	change *dockerbuildstate.StateChange
	ctx    context.Context
	fn     func(ctx context.Context) (imageID string, err error)
	done   chan struct{}
}

// ── Actor ─────────────────────────────────────────────────────────────────────

// Actor owns the async dispatch loop and is the single point of contact for
// callers that want to build images or manage the build cache.
type Actor struct {
	ops   opsProvider
	state *dockerbuildstate.State
	log   *logger.Logger
	cfg   Config

	queue     chan workItem
	closed    chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// New creates an Actor and immediately starts cfg.Workers background workers.
// Callers must call Close when finished.
func New(ops opsProvider, st *dockerbuildstate.State, log *logger.Logger, cfg *Config) *Actor {
	log.LowTrace("creating build actor")
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

	log.Info("build actor ready (workers=%d queueSize=%d)", c.Workers, c.QueueSize)
	return a
}

// ── Mutating operations ───────────────────────────────────────────────────────

// Build submits an async image build.  State is updated to active (with the
// resulting image ID) on success, or failed on error.
func (a *Actor) Build(ctx context.Context, req dockerbuildops.BuildRequest) (*Ticket, error) {
	tags := strings.Join(req.Tags, ",")
	a.log.LowTrace("submitting build: tags=%q dockerfile=%q", tags, req.Dockerfile)

	meta := map[string]string{
		"dockerfile": req.Dockerfile,
		"target":     req.Target,
		"platform":   req.Platform,
	}
	if req.NoCache {
		meta["no_cache"] = "true"
	}
	if req.RemoteContext != "" {
		meta["remote_context"] = req.RemoteContext
	}

	change := a.state.RequestChange(dockerbuildstate.OpBuild, tags, meta)
	a.log.Trace("build change registered: id=%s", change.ID)

	fn := func(ctx context.Context) (string, error) {
		a.log.Debug("worker: executing build (change=%s tags=%q)", change.ID, tags)

		resp, err := a.ops.Build(ctx, req)
		if err != nil {
			a.log.Error("worker: build failed (change=%s): %v", change.ID, err)
			return "", err
		}

		a.log.Debug("worker: build succeeded (change=%s) → imageID=%s", change.ID, resp.ImageID)
		return resp.ImageID, nil
	}

	return a.submit(change, ctx, fn)
}

// PruneCache submits an async build-cache prune.
func (a *Actor) PruneCache(ctx context.Context, opts dockertypes.BuildCachePruneOptions) (*Ticket, error) {
	a.log.LowTrace("submitting build cache prune: all=%v keepStorage=%d", opts.All, opts.KeepStorage)

	meta := map[string]string{
		"all":          fmt.Sprintf("%v", opts.All),
		"keep_storage": fmt.Sprintf("%d", opts.KeepStorage),
	}

	change := a.state.RequestChange(dockerbuildstate.OpPruneCache, "", meta)
	a.log.Trace("prune-cache change registered: id=%s", change.ID)

	fn := func(ctx context.Context) (string, error) {
		a.log.Debug("worker: executing build cache prune (change=%s)", change.ID)

		report, err := a.ops.PruneCache(ctx, opts)
		if err != nil {
			a.log.Error("worker: prune-cache failed (change=%s): %v", change.ID, err)
			return "", err
		}

		a.log.Debug("worker: prune-cache succeeded: deleted=%d spaceReclaimed=%d",
			len(report.CachesDeleted), report.SpaceReclaimed)
		return "", nil
	}

	return a.submit(change, ctx, fn)
}

// ── Shutdown ──────────────────────────────────────────────────────────────────

// Close stops the actor gracefully.  In-flight work already picked up by
// workers runs to completion; items still sitting in the queue are abandoned.
// Close is safe to call multiple times; subsequent calls are no-ops.
func (a *Actor) Close() {
	a.closeOnce.Do(func() {
		a.log.LowTrace("closing build actor")
		a.log.Debug("signalling workers to stop and draining queue")
		close(a.closed)
		a.wg.Wait()
		a.log.Info("build actor closed, all workers exited")
	})
}

// ── Internal helpers ──────────────────────────────────────────────────────────

func (a *Actor) submit(change *dockerbuildstate.StateChange, ctx context.Context, fn func(context.Context) (string, error)) (*Ticket, error) {
	a.log.Trace("submit: change=%s tags=%q", change.ID, change.Tags)

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
		a.log.Warn("submit: queue full, abandoning change %s (op=%s tags=%q)",
			change.ID, change.Op, change.Tags)
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
			a.log.Trace("worker %d: picked up change %s (op=%s tags=%q)",
				workerID, item.change.ID, item.change.Op, item.change.Tags)
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
	a.log.Debug("worker %d: executing change %s (op=%s tags=%q)",
		workerID, changeID, item.change.Op, item.change.Tags)

	select {
	case <-item.ctx.Done():
		reason := fmt.Sprintf("context done before execution: %v", item.ctx.Err())
		a.log.Warn("worker %d: %s (change=%s)", workerID, reason, changeID)
		_, _ = a.state.Abandon(changeID, reason)
		return
	default:
	}

	imageID, err := item.fn(item.ctx)
	if err != nil {
		a.log.Error("worker %d: change %s failed: %v", workerID, changeID, err)
		_, _ = a.state.RecordFailure(changeID, err)
		return
	}

	a.log.Debug("worker %d: change %s succeeded (imageID=%q)", workerID, changeID, imageID)
	_, _ = a.state.ConfirmSuccess(changeID, imageID)
	a.log.Info("worker %d: change %s settled → active", workerID, changeID)
}
