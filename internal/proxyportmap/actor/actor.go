// Package proxyportmapactor dispatches port-map operations asynchronously.
//
// Flow:
//
//	caller → Actor.ScanAndMap()
//	             → state.RequestChange()   [immediate]
//	             → enqueue work item       [immediate, non-blocking]
//	             ← *Ticket{ChangeID, Done}
//
//	             … worker picks up work …
//	             → ops.ScanListeningPorts()
//	             → portProxy.EnsureProxy() + Wait()
//	             → portProxy.RegisterContainer() + Wait()
//	             → store.SetResult()
//	             → state.ConfirmSuccess() or RecordFailure()
//	             → close(Ticket.Done)
package proxyportmapactor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	portproxyactor "dokoko.ai/dokoko/internal/portproxy/actor"
	portproxystate "dokoko.ai/dokoko/internal/portproxy/state"
	proxyportmapops "dokoko.ai/dokoko/internal/proxyportmap/ops"
	proxyportmapstate "dokoko.ai/dokoko/internal/proxyportmap/state"
	"dokoko.ai/dokoko/pkg/logger"
)

// ── Errors ────────────────────────────────────────────────────────────────────

var (
	ErrQueueFull   = errors.New("proxyportmap actor work queue full")
	ErrActorClosed = errors.New("proxyportmap actor is closed")
)

// ── Config ────────────────────────────────────────────────────────────────────

type Config struct {
	Workers   int
	QueueSize int
}

func (c *Config) withDefaults() Config {
	out := Config{Workers: 2, QueueSize: 32}
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

// ── Dependency interfaces ─────────────────────────────────────────────────────

// opsProvider is the Docker-exec layer for port scanning.
type opsProvider interface {
	ScanListeningPorts(ctx context.Context, containerName string) ([]proxyportmapops.PortInfo, error)
}

// proxyRegistrar is the portproxy clerk interface needed by this actor.
// *portproxyclerk.Clerk satisfies this via structural typing.
type proxyRegistrar interface {
	EnsureProxy(ctx context.Context) (*portproxyactor.Ticket, error)
	RegisterContainer(ctx context.Context, name, id string, ports []portproxystate.ContainerPort) (*portproxyactor.Ticket, error)
	DeregisterContainer(ctx context.Context, name string) (*portproxyactor.Ticket, error)
	Store() *portproxystate.Store
}

// ── Ticket ────────────────────────────────────────────────────────────────────

// Ticket is returned immediately by every mutating operation.
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

// ── Internal work unit ────────────────────────────────────────────────────────

type workItem struct {
	change *proxyportmapstate.StateChange
	ctx    context.Context
	fn     func(ctx context.Context) (string, error)
	done   chan struct{}
}

// ── Actor ─────────────────────────────────────────────────────────────────────

// Actor owns the async dispatch loop for port-map operations.
type Actor struct {
	ops        opsProvider
	portProxy  proxyRegistrar
	state      *proxyportmapstate.State
	store      *proxyportmapstate.Store
	log        *logger.Logger
	cfg        Config
	queue      chan workItem
	closed     chan struct{}
	closeOnce  sync.Once
	wg         sync.WaitGroup
}

// New creates an Actor and starts cfg.Workers background goroutines.
func New(
	ops opsProvider,
	pp proxyRegistrar,
	st *proxyportmapstate.State,
	store *proxyportmapstate.Store,
	log *logger.Logger,
	cfg *Config,
) *Actor {
	log.LowTrace("creating proxyportmap actor")
	c := cfg.withDefaults()

	a := &Actor{
		ops:       ops,
		portProxy: pp,
		state:     st,
		store:     store,
		log:       log,
		cfg:       c,
		queue:     make(chan workItem, c.QueueSize),
		closed:    make(chan struct{}),
	}

	for i := range c.Workers {
		a.wg.Add(1)
		go a.runWorker(i)
	}

	log.Info("proxyportmap actor ready (workers=%d queueSize=%d)", c.Workers, c.QueueSize)
	return a
}

// ── Mutating operations ───────────────────────────────────────────────────────

// ScanAndMap scans the container for listening TCP ports, registers them with
// the nginx proxy, and stores the combined result.
func (a *Actor) ScanAndMap(ctx context.Context, userID, containerName, containerID string) (*Ticket, error) {
	a.log.LowTrace("proxyportmap actor: submitting ScanAndMap user=%s container=%s", userID, containerName)

	meta := map[string]string{
		"container_name": containerName,
		"container_id":   containerID,
	}
	change := a.state.RequestChange(proxyportmapstate.OpScanAndMap, userID, meta)

	fn := func(ctx context.Context) (string, error) {
		// 1. Scan container ports.
		rawPorts, err := a.ops.ScanListeningPorts(ctx, containerName)
		if err != nil {
			return "", fmt.Errorf("scan ports: %w", err)
		}

		// 2. Store result immediately — even with no host-port mapping yet.
		//    This guarantees the user always sees found ports regardless of
		//    whether proxy registration succeeds later.
		rawMapped := make([]proxyportmapstate.MappedPort, len(rawPorts))
		for i, info := range rawPorts {
			rawMapped[i] = proxyportmapstate.MappedPort{ContainerPort: info.Port, Process: info.Process}
		}
		a.store.SetResult(&proxyportmapstate.ScanResult{
			UserID:        userID,
			ContainerName: containerName,
			ContainerID:   containerID,
			Ports:         rawMapped,
			ScannedAt:     time.Now(),
		})

		if len(rawPorts) == 0 {
			return "0 ports", nil
		}

		// 3. Ensure the proxy container is running.
		ept, err := a.portProxy.EnsureProxy(ctx)
		if err != nil {
			a.log.Warn("proxyportmap: ensure proxy failed (ports found but not proxied): %v", err)
			return fmt.Sprintf("%d ports (proxy unavailable)", len(rawPorts)), nil
		}
		if err := ept.Wait(ctx); err != nil {
			a.log.Warn("proxyportmap: ensure proxy wait failed (ports found but not proxied): %v", err)
			return fmt.Sprintf("%d ports (proxy unavailable)", len(rawPorts)), nil
		}

		// 4. Register ports with the proxy.
		cports := make([]portproxystate.ContainerPort, len(rawPorts))
		for i, info := range rawPorts {
			cports[i] = portproxystate.ContainerPort{Port: info.Port, Proto: "tcp"}
		}
		rt, err := a.portProxy.RegisterContainer(ctx, containerName, containerID, cports)
		if err != nil {
			a.log.Warn("proxyportmap: register container failed (ports found but not proxied): %v", err)
			return fmt.Sprintf("%d ports (proxy registration failed)", len(rawPorts)), nil
		}
		if err := rt.Wait(ctx); err != nil {
			a.log.Warn("proxyportmap: register container wait failed (ports found but not proxied): %v", err)
			return fmt.Sprintf("%d ports (proxy registration failed)", len(rawPorts)), nil
		}

		// 5. Update result with host-port mappings from the portproxy store.
		// Build a port → process name lookup so we can preserve process info.
		portToProcess := make(map[uint16]string, len(rawPorts))
		for _, info := range rawPorts {
			portToProcess[info.Port] = info.Process
		}

		mappings := a.portProxy.Store().GetByContainer(containerName)
		mapped := make([]proxyportmapstate.MappedPort, 0, len(mappings))
		for _, m := range mappings {
			if m.Status != portproxystate.MappingStatusActive {
				continue
			}
			// Skip ports no longer in the current scan — they've stopped listening.
			if _, stillListening := portToProcess[m.ContainerPort.Port]; !stillListening {
				continue
			}
			mapped = append(mapped, proxyportmapstate.MappedPort{
				ContainerPort: m.ContainerPort.Port,
				HostPort:      m.HostPort,
				Process:       portToProcess[m.ContainerPort.Port],
			})
		}

		a.store.SetResult(&proxyportmapstate.ScanResult{
			UserID:        userID,
			ContainerName: containerName,
			ContainerID:   containerID,
			Ports:         mapped,
			ScannedAt:     time.Now(),
		})

		return fmt.Sprintf("%d ports", len(mapped)), nil
	}

	return a.submit(change, ctx, fn)
}

// Unmap deregisters the container from the nginx proxy and clears the stored
// result.  The containerName is read from the store so callers only need
// the userID.
func (a *Actor) Unmap(ctx context.Context, userID string) (*Ticket, error) {
	a.log.LowTrace("proxyportmap actor: submitting Unmap user=%s", userID)

	change := a.state.RequestChange(proxyportmapstate.OpUnmap, userID, nil)

	fn := func(ctx context.Context) (string, error) {
		result := a.store.GetResult(userID)
		if result == nil {
			return "no mapping", nil
		}

		dt, err := a.portProxy.DeregisterContainer(ctx, result.ContainerName)
		if err != nil {
			return "", fmt.Errorf("deregister container: %w", err)
		}
		if err := dt.Wait(ctx); err != nil {
			return "", fmt.Errorf("deregister container wait: %w", err)
		}

		a.store.DeleteResult(userID)
		return "unmapped", nil
	}

	return a.submit(change, ctx, fn)
}

// Close stops the actor gracefully.
func (a *Actor) Close() {
	a.closeOnce.Do(func() {
		a.log.LowTrace("closing proxyportmap actor")
		close(a.closed)
		a.wg.Wait()
		a.log.Info("proxyportmap actor closed")
	})
}

// ── Internal helpers ──────────────────────────────────────────────────────────

func (a *Actor) submit(change *proxyportmapstate.StateChange, ctx context.Context, fn func(context.Context) (string, error)) (*Ticket, error) {
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
	a.log.Debug("proxyportmap worker %d started", id)
	for {
		select {
		case item, ok := <-a.queue:
			if !ok {
				return
			}
			a.execute(id, item)
		case <-a.closed:
			a.drainQueue()
			return
		}
	}
}

func (a *Actor) drainQueue() {
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
		_, _ = a.state.Abandon(item.change.ID, fmt.Sprintf("context done: %v", item.ctx.Err()))
		return
	default:
	}

	resultRef, err := item.fn(item.ctx)
	if err != nil {
		a.log.Error("proxyportmap worker %d: change %s failed: %v", id, item.change.ID, err)
		_, _ = a.state.RecordFailure(item.change.ID, err)
		return
	}

	_, _ = a.state.ConfirmSuccess(item.change.ID, resultRef)
	a.log.Info("proxyportmap worker %d: change %s settled → active (result=%q)", id, item.change.ID, resultRef)
}
