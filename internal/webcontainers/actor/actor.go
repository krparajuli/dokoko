// Package webcontainersactor dispatches web-container operations asynchronously.
//
// Flow for mutating operations (Provision, Terminate):
//
//	caller → Actor.Provision()
//	            → state.RequestChange()       [immediate, on caller goroutine]
//	            → enqueue work item           [immediate, non-blocking]
//	            ← *Ticket{ChangeID, Done}     [returned to caller immediately]
//
//	            … worker goroutine picks up work …
//	            → ops.ProvisionContainer() / ops.TerminateContainer()
//	            → store.SetSession()          (on success)
//	            → state.ConfirmSuccess() / RecordFailure() / Abandon()
//	            → close(Ticket.Done)
package webcontainersactor

import (
	"context"
	"errors"
	"fmt"
	"sync"

	webcontainerscatalog "dokoko.ai/dokoko/internal/webcontainers/catalog"
	webcontainersops "dokoko.ai/dokoko/internal/webcontainers/ops"
	webcontainersstate "dokoko.ai/dokoko/internal/webcontainers/state"
	"dokoko.ai/dokoko/pkg/logger"
)

// ── Errors ────────────────────────────────────────────────────────────────────

var (
	ErrQueueFull   = errors.New("webcontainer actor work queue full")
	ErrActorClosed = errors.New("webcontainer actor is closed")
)

// ── Configuration ─────────────────────────────────────────────────────────────

type Config struct {
	Workers   int // default 4
	QueueSize int // default 64
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

// ── opsProvider ───────────────────────────────────────────────────────────────

type opsProvider interface {
	ProvisionContainer(ctx context.Context, userID string, def *webcontainerscatalog.ImageDef, extraEnv map[string]string) (*webcontainersops.ProvisionResult, error)
	TerminateContainer(ctx context.Context, containerName string) error
}

// ── Ticket ────────────────────────────────────────────────────────────────────

// Ticket is returned immediately by every mutating operation.
// Done is closed once the underlying work has settled.
type Ticket struct {
	ChangeID string
	Done     <-chan struct{}
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

// ── workItem ──────────────────────────────────────────────────────────────────

type workItem struct {
	change *webcontainersstate.StateChange
	ctx    context.Context
	fn     func(ctx context.Context) (resultRef string, err error)
	done   chan struct{}
}

// ── Actor ─────────────────────────────────────────────────────────────────────

// Actor owns the async dispatch loop for web-container operations.
type Actor struct {
	ops   opsProvider
	state *webcontainersstate.State
	store *webcontainersstate.Store
	log   *logger.Logger
	cfg   Config

	queue     chan workItem
	closed    chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// New creates an Actor and starts cfg.Workers background workers.
func New(ops opsProvider, st *webcontainersstate.State, store *webcontainersstate.Store, log *logger.Logger, cfg *Config) *Actor {
	c := cfg.withDefaults()
	a := &Actor{
		ops:    ops,
		state:  st,
		store:  store,
		log:    log,
		cfg:    c,
		queue:  make(chan workItem, c.QueueSize),
		closed: make(chan struct{}),
	}
	for i := range c.Workers {
		a.wg.Add(1)
		go a.runWorker(i)
	}
	log.Info("webcontainer actor ready (workers=%d queueSize=%d)", c.Workers, c.QueueSize)
	return a
}

// ── Mutating operations ───────────────────────────────────────────────────────

// Provision asynchronously provisions a container for userID using def.
// envVars are injected into the container's environment at creation time.
// The store is updated on success with a UserSession in StatusReady.
func (a *Actor) Provision(ctx context.Context, userID string, def *webcontainerscatalog.ImageDef, envVars map[string]string) (*Ticket, error) {
	a.log.LowTrace("webcontainer actor: submitting provision for user=%s image=%s", userID, def.Image)

	meta := map[string]string{"catalog_id": def.ID, "image": def.Image}
	change := a.state.RequestChange(webcontainersstate.OpProvision, userID, meta)

	// Write a "provisioning" sentinel so callers can poll for status.
	a.store.SetSession(&webcontainersstate.UserSession{
		UserID:    userID,
		CatalogID: def.ID,
		Status:    webcontainersstate.StatusProvisioning,
	})

	fn := func(ctx context.Context) (string, error) {
		a.log.Debug("webcontainer actor: provisioning user=%s", userID)
		res, err := a.ops.ProvisionContainer(ctx, userID, def, envVars)
		if err != nil {
			a.store.SetSession(&webcontainersstate.UserSession{
				UserID:    userID,
				CatalogID: def.ID,
				Status:    webcontainersstate.StatusError,
				ErrorMsg:  err.Error(),
			})
			return "", err
		}
		a.store.SetSession(&webcontainersstate.UserSession{
			UserID:        userID,
			CatalogID:     def.ID,
			ContainerName: res.ContainerName,
			ContainerID:   res.ContainerID,
			HostPort:      res.HostPort,
			Status:        webcontainersstate.StatusReady,
		})
		return fmt.Sprintf("%s:%d", res.ContainerID, res.HostPort), nil
	}

	return a.submit(change, ctx, fn)
}

// Terminate asynchronously stops and removes a user's container.
func (a *Actor) Terminate(ctx context.Context, userID, containerName string) (*Ticket, error) {
	a.log.LowTrace("webcontainer actor: submitting terminate for user=%s", userID)

	change := a.state.RequestChange(webcontainersstate.OpTerminate, userID, nil)

	// Mark as terminating immediately.
	if sess := a.store.GetSession(userID); sess != nil {
		sess.Status = webcontainersstate.StatusTerminating
		a.store.SetSession(sess)
	}

	fn := func(ctx context.Context) (string, error) {
		a.log.Debug("webcontainer actor: terminating container %s for user=%s", containerName, userID)
		if err := a.ops.TerminateContainer(ctx, containerName); err != nil {
			return "", err
		}
		a.store.DeleteSession(userID)
		return "", nil
	}

	return a.submit(change, ctx, fn)
}

// ── Shutdown ──────────────────────────────────────────────────────────────────

// Close stops the actor gracefully.  In-flight work runs to completion;
// queued items are abandoned.  Safe to call multiple times.
func (a *Actor) Close() {
	a.closeOnce.Do(func() {
		a.log.LowTrace("closing webcontainer actor")
		close(a.closed)
		a.wg.Wait()
		a.log.Info("webcontainer actor closed")
	})
}

// ── Internal helpers ──────────────────────────────────────────────────────────

func (a *Actor) submit(change *webcontainersstate.StateChange, ctx context.Context, fn func(context.Context) (string, error)) (*Ticket, error) {
	select {
	case <-a.closed:
		_, _ = a.state.Abandon(change.ID, "actor closed")
		return nil, ErrActorClosed
	default:
	}

	done := make(chan struct{})
	item := workItem{change: change, ctx: ctx, fn: fn, done: done}

	select {
	case a.queue <- item:
		return &Ticket{ChangeID: change.ID, Done: done}, nil
	case <-a.closed:
		_, _ = a.state.Abandon(change.ID, "actor closed")
		close(done)
		return nil, ErrActorClosed
	default:
		_, _ = a.state.Abandon(change.ID, "work queue full")
		close(done)
		return nil, ErrQueueFull
	}
}

func (a *Actor) runWorker(id int) {
	defer a.wg.Done()
	a.log.Debug("webcontainer worker %d started", id)

	for {
		select {
		case item, ok := <-a.queue:
			if !ok {
				return
			}
			a.execute(id, item)
		case <-a.closed:
			a.drainQueue(id)
			return
		}
	}
}

func (a *Actor) drainQueue(id int) {
	for {
		select {
		case item := <-a.queue:
			_, _ = a.state.Abandon(item.change.ID, "actor shutting down")
			close(item.done)
		default:
			return
		}
	}
}

func (a *Actor) execute(id int, item workItem) {
	defer close(item.done)

	select {
	case <-item.ctx.Done():
		reason := fmt.Sprintf("context done before execution: %v", item.ctx.Err())
		_, _ = a.state.Abandon(item.change.ID, reason)
		return
	default:
	}

	resultRef, err := item.fn(item.ctx)
	if err != nil {
		a.log.Error("webcontainer worker %d: change %s failed: %v", id, item.change.ID, err)
		_, _ = a.state.RecordFailure(item.change.ID, err)
		return
	}

	_, _ = a.state.ConfirmSuccess(item.change.ID, resultRef)
	a.log.Info("webcontainer worker %d: change %s settled → active", id, item.change.ID)
}
