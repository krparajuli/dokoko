// Package portproxyactor dispatches port-proxy operations asynchronously,
// keeping portproxystate in sync with every outcome.
//
// Flow for mutating operations:
//
//	caller → Actor.EnsureProxy()
//	            → state.RequestChange()       [immediate, on caller goroutine]
//	            → enqueue work item           [immediate, non-blocking]
//	            ← *Ticket{ChangeID, Done}     [returned to caller immediately]
//
//	            … worker goroutine picks up work …
//	            → ops call(s)
//	            → state.ConfirmSuccess()  (on success)
//	            → state.RecordFailure()   (on ops error)
//	            → state.Abandon()         (if context was cancelled before execution)
//	            → close(Ticket.Done)
package portproxyactor

import (
	"context"
	"errors"
	"fmt"
	"sync"

	portproxyconfig "dokoko.ai/dokoko/internal/portproxy/config"
	portproxystate "dokoko.ai/dokoko/internal/portproxy/state"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
)

// ── Errors ────────────────────────────────────────────────────────────────────

var (
	// ErrQueueFull is returned when the internal work queue is at capacity.
	ErrQueueFull = errors.New("portproxy actor work queue full")

	// ErrActorClosed is returned when a mutating operation is submitted to a
	// stopped actor.
	ErrActorClosed = errors.New("portproxy actor is closed")
)

// ── Configuration ─────────────────────────────────────────────────────────────

// Config controls Actor behaviour. All fields have sane defaults.
type Config struct {
	// Workers is the number of goroutines that drain the work queue.
	// Default: 4.
	Workers int

	// QueueSize is the maximum number of pending items before submits are
	// abandoned immediately. Default: 64.
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

type opsProvider interface {
	EnsureProxyContainer(ctx context.Context) (string, error)
	CreateProxyNetwork(ctx context.Context, containerName string) (string, error)
	ConnectToProxyNetwork(ctx context.Context, networkID, proxyID, managedID, managedName string) error
	DisconnectFromProxyNetwork(ctx context.Context, networkName, proxyID, managedID string) error
	ReloadNginxConfig(ctx context.Context, proxyID, newConfig string) error
	InspectContainer(ctx context.Context, ref string) (dockertypes.ContainerJSON, error)
}

// ── Ticket ────────────────────────────────────────────────────────────────────

// Ticket is returned immediately by every mutating operation.
// Done is closed once the underlying work has settled.
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
	change *portproxystate.StateChange
	ctx    context.Context
	fn     func(ctx context.Context) (resultRef string, err error)
	done   chan struct{}
}

// ── Actor ─────────────────────────────────────────────────────────────────────

// Actor owns the async dispatch loop for port-proxy operations.
type Actor struct {
	ops   opsProvider
	state *portproxystate.State
	store *portproxystate.Store
	log   *logger.Logger
	cfg   Config

	queue     chan workItem
	closed    chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// New creates an Actor and immediately starts cfg.Workers background workers.
// Callers must call Close when finished.
func New(ops opsProvider, st *portproxystate.State, store *portproxystate.Store, log *logger.Logger, cfg *Config) *Actor {
	log.LowTrace("creating portproxy actor")
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

	log.Info("portproxy actor ready (workers=%d queueSize=%d)", c.Workers, c.QueueSize)
	return a
}

// ── Mutating operations ───────────────────────────────────────────────────────

// EnsureProxy submits an async operation to ensure the proxy container is running.
func (a *Actor) EnsureProxy(ctx context.Context) (*Ticket, error) {
	a.log.LowTrace("portproxy actor: submitting EnsureProxy")

	change := a.state.RequestChange(portproxystate.OpEnsureProxy, "proxy", nil)

	fn := func(ctx context.Context) (string, error) {
		containerID, err := a.ops.EnsureProxyContainer(ctx)
		if err != nil {
			return "", err
		}
		return containerID, nil
	}

	return a.submit(change, ctx, fn)
}

// RegisterContainer submits an async operation to register a container with
// the proxy: allocate host ports, create a bridge network, connect both
// containers, and reload nginx.
func (a *Actor) RegisterContainer(ctx context.Context, name, id string, ports []portproxystate.ContainerPort) (*Ticket, error) {
	a.log.LowTrace("portproxy actor: submitting RegisterContainer name=%s ports=%d", name, len(ports))

	meta := map[string]string{
		"id":        id,
		"portCount": fmt.Sprintf("%d", len(ports)),
	}
	change := a.state.RequestChange(portproxystate.OpRegisterContainer, name, meta)

	fn := func(ctx context.Context) (string, error) {
		// 1. Inspect the proxy container to get its ID.
		proxyInfo, err := a.ops.InspectContainer(ctx, "dokoko-proxy")
		if err != nil {
			return "", fmt.Errorf("inspect proxy container: %w", err)
		}
		proxyID := proxyInfo.ID

		// 2. Allocate host ports (with rollback on failure).
		var allocated []*portproxystate.PortMapping
		for _, cp := range ports {
			if cp.Proto != "tcp" {
				continue
			}
			m, err := a.store.AllocatePort(name, id, cp)
			if err != nil {
				// Rollback all successfully allocated ports.
				for _, prev := range allocated {
					a.store.ReleaseMappingsFor(prev.ContainerName)
				}
				return "", fmt.Errorf("allocate port for %s:%d/%s: %w", name, cp.Port, cp.Proto, err)
			}
			allocated = append(allocated, m)
		}

		if len(allocated) == 0 {
			return "0 ports", nil
		}

		// 3. Create a dedicated bridge network.
		networkID, err := a.ops.CreateProxyNetwork(ctx, name)
		if err != nil {
			a.store.ReleaseMappingsFor(name)
			return "", fmt.Errorf("create proxy network for %s: %w", name, err)
		}

		// 4. Connect proxy and managed container to the bridge network.
		if err := a.ops.ConnectToProxyNetwork(ctx, networkID, proxyID, id, name); err != nil {
			a.store.ReleaseMappingsFor(name)
			_ = a.ops.DisconnectFromProxyNetwork(ctx, "proxy_"+name, proxyID, id)
			return "", fmt.Errorf("connect to proxy network for %s: %w", name, err)
		}

		// 5. Regenerate and reload nginx config (non-fatal on failure).
		newCfg := portproxyconfig.Generate(a.store.AllActive())
		if err := a.ops.ReloadNginxConfig(ctx, proxyID, newCfg); err != nil {
			a.log.Warn("portproxy actor: nginx reload failed (non-fatal): %v", err)
		}

		return fmt.Sprintf("%d ports", len(allocated)), nil
	}

	return a.submit(change, ctx, fn)
}

// DeregisterContainer submits an async operation to deregister a container:
// release host ports, disconnect from the proxy network, and reload nginx.
func (a *Actor) DeregisterContainer(ctx context.Context, name string) (*Ticket, error) {
	a.log.LowTrace("portproxy actor: submitting DeregisterContainer name=%s", name)

	change := a.state.RequestChange(portproxystate.OpDeregisterContainer, name, nil)

	fn := func(ctx context.Context) (string, error) {
		mappings := a.store.GetByContainer(name)
		if len(mappings) == 0 {
			a.log.Debug("portproxy actor: no mappings for %s — nothing to deregister", name)
			return "no mappings", nil
		}

		// Get container ID from the first mapping.
		containerID := mappings[0].ContainerID

		// Get the proxy container ID (best-effort — skip reload on error).
		var proxyID string
		proxyInfo, err := a.ops.InspectContainer(ctx, "dokoko-proxy")
		if err != nil {
			a.log.Warn("portproxy actor: inspect proxy failed during deregister: %v", err)
		} else {
			proxyID = proxyInfo.ID
		}

		// Disconnect and remove the network (best-effort).
		_ = a.ops.DisconnectFromProxyNetwork(ctx, "proxy_"+name, proxyID, containerID)

		// Release port mappings.
		freed := a.store.ReleaseMappingsFor(name)

		// Reload nginx config (best-effort).
		if proxyID != "" {
			newCfg := portproxyconfig.Generate(a.store.AllActive())
			if err := a.ops.ReloadNginxConfig(ctx, proxyID, newCfg); err != nil {
				a.log.Warn("portproxy actor: nginx reload failed during deregister (non-fatal): %v", err)
			}
		}

		return fmt.Sprintf("freed %d ports", len(freed)), nil
	}

	return a.submit(change, ctx, fn)
}

// ── Shutdown ──────────────────────────────────────────────────────────────────

// Close stops the actor gracefully. In-flight work already picked up by
// workers runs to completion; items still sitting in the queue are abandoned.
func (a *Actor) Close() {
	a.closeOnce.Do(func() {
		a.log.LowTrace("closing portproxy actor")
		close(a.closed)
		a.wg.Wait()
		a.log.Info("portproxy actor closed")
	})
}

// ── Internal helpers ──────────────────────────────────────────────────────────

func (a *Actor) submit(change *portproxystate.StateChange, ctx context.Context, fn func(context.Context) (string, error)) (*Ticket, error) {
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

func (a *Actor) runWorker(workerID int) {
	defer a.wg.Done()
	a.log.Debug("portproxy worker %d started", workerID)

	for {
		select {
		case item, ok := <-a.queue:
			if !ok {
				return
			}
			a.execute(workerID, item)

		case <-a.closed:
			a.drainQueue(workerID)
			return
		}
	}
}

func (a *Actor) drainQueue(workerID int) {
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

func (a *Actor) execute(workerID int, item workItem) {
	defer close(item.done)

	changeID := item.change.ID

	select {
	case <-item.ctx.Done():
		reason := fmt.Sprintf("context done before execution: %v", item.ctx.Err())
		_, _ = a.state.Abandon(changeID, reason)
		return
	default:
	}

	resultRef, err := item.fn(item.ctx)
	if err != nil {
		a.log.Error("portproxy worker %d: change %s failed: %v", workerID, changeID, err)
		_, _ = a.state.RecordFailure(changeID, err)
		return
	}

	_, _ = a.state.ConfirmSuccess(changeID, resultRef)
	a.log.Info("portproxy worker %d: change %s settled → active (result=%q)", workerID, changeID, resultRef)
}
