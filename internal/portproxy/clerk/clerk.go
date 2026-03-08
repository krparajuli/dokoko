// Package portproxyclerk provides the public API facade for the port-proxy
// subsystem.  It wires the actor, state, and store together.
package portproxyclerk

import (
	"context"

	portproxyactor "dokoko.ai/dokoko/internal/portproxy/actor"
	portproxystate "dokoko.ai/dokoko/internal/portproxy/state"
	"dokoko.ai/dokoko/pkg/logger"
	dockernat "github.com/docker/go-connections/nat"
)

// Actor is the interface the Clerk uses for all port-proxy mutations.
type Actor interface {
	EnsureProxy(ctx context.Context) (*portproxyactor.Ticket, error)
	RegisterContainer(ctx context.Context, name, id string, ports []portproxystate.ContainerPort) (*portproxyactor.Ticket, error)
	DeregisterContainer(ctx context.Context, name string) (*portproxyactor.Ticket, error)
	Close()
}

// Clerk is the single entry point for callers that need to interact with the
// port-proxy subsystem.  All exported methods are safe for concurrent use.
type Clerk struct {
	actor Actor
	state *portproxystate.State
	store *portproxystate.Store
	log   *logger.Logger
}

// New returns a Clerk that wraps the given actor, state, and store.
func New(act Actor, st *portproxystate.State, store *portproxystate.Store, log *logger.Logger) *Clerk {
	log.LowTrace("creating portproxy clerk")
	return &Clerk{actor: act, state: st, store: store, log: log}
}

// ── Accessors ─────────────────────────────────────────────────────────────────

// State returns the mutation-lifecycle State.
func (c *Clerk) State() *portproxystate.State { return c.state }

// Store returns the port-mapping Store.
func (c *Clerk) Store() *portproxystate.Store { return c.store }

// ── Mutations ─────────────────────────────────────────────────────────────────

// EnsureProxy submits an async operation to ensure the proxy container is
// running.
func (c *Clerk) EnsureProxy(ctx context.Context) (*portproxyactor.Ticket, error) {
	c.log.LowTrace("portproxy clerk: EnsureProxy")
	return c.actor.EnsureProxy(ctx)
}

// RegisterContainer submits an async operation to register a container with
// the proxy (allocate ports, create network, connect, reload nginx).
func (c *Clerk) RegisterContainer(ctx context.Context, name, id string, ports []portproxystate.ContainerPort) (*portproxyactor.Ticket, error) {
	c.log.LowTrace("portproxy clerk: RegisterContainer name=%s ports=%d", name, len(ports))
	return c.actor.RegisterContainer(ctx, name, id, ports)
}

// DeregisterContainer submits an async operation to deregister a container
// from the proxy (release ports, remove network, reload nginx).
func (c *Clerk) DeregisterContainer(ctx context.Context, name string) (*portproxyactor.Ticket, error) {
	c.log.LowTrace("portproxy clerk: DeregisterContainer name=%s", name)
	return c.actor.DeregisterContainer(ctx, name)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// ParseExposedPorts converts a nat.PortSet into a []ContainerPort, keeping
// only TCP ports.
func ParseExposedPorts(ps dockernat.PortSet) []portproxystate.ContainerPort {
	out := make([]portproxystate.ContainerPort, 0, len(ps))
	for port := range ps {
		if port.Proto() != "tcp" {
			continue
		}
		out = append(out, portproxystate.ContainerPort{
			Port:  uint16(port.Int()),
			Proto: "tcp",
		})
	}
	return out
}
