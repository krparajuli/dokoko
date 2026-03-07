// Package dockernetworkclerk provides a Clerk that acts as a proxy between an
// outside manager and the Docker network subsystem.
//
// The Clerk wires three existing layers together:
//
//   - Actor — async mutation dispatch (Create, Remove, Prune) and read-only queries
//   - State — in-flight mutation lifecycle (requested / active / failed / abandoned)
//   - Store — persistent network inventory (Register, Reconcile, Mark*)
package dockernetworkclerk

import (
	"context"

	dockernetworkactor "dokoko.ai/dokoko/internal/docker/networks/actor"
	dockernetworkstate "dokoko.ai/dokoko/internal/docker/networks/state"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
	dockerfilters "github.com/docker/docker/api/types/filters"
)

// Actor is the interface the Clerk uses for all network operations.
// *dockernetworkactor.Actor satisfies this interface; tests may inject a fake.
type Actor interface {
	Create(ctx context.Context, name string, opts dockertypes.NetworkCreate) (*dockernetworkactor.Ticket, error)
	Remove(ctx context.Context, networkID string) (*dockernetworkactor.Ticket, error)
	Prune(ctx context.Context, pruneFilter dockerfilters.Args) (*dockernetworkactor.Ticket, error)
	List(ctx context.Context, opts dockertypes.NetworkListOptions) <-chan dockernetworkactor.ListResult
	Inspect(ctx context.Context, networkID string, opts dockertypes.NetworkInspectOptions) <-chan dockernetworkactor.InspectResult
}

// Clerk is the single entry point for an outside manager that needs to
// interact with Docker networks.  It composes the actor (mutations), state
// (lifecycle tracking), and store (network inventory).
//
// All exported methods are safe for concurrent use.
type Clerk struct {
	actor Actor
	state *dockernetworkstate.State
	store *dockernetworkstate.NetworkStore
	log   *logger.Logger
}

// New returns a Clerk that wraps act, st, and store.
func New(act Actor, st *dockernetworkstate.State, store *dockernetworkstate.NetworkStore, log *logger.Logger) *Clerk {
	log.LowTrace("creating network clerk")
	return &Clerk{actor: act, state: st, store: store, log: log}
}

// ── Accessors ─────────────────────────────────────────────────────────────────

// State returns the mutation-lifecycle State.
func (c *Clerk) State() *dockernetworkstate.State { return c.state }

// Store returns the network inventory NetworkStore.
func (c *Clerk) Store() *dockernetworkstate.NetworkStore { return c.store }

// ── Mutations ─────────────────────────────────────────────────────────────────

// Create submits an async network creation.
func (c *Clerk) Create(ctx context.Context, name string, opts dockertypes.NetworkCreate) (*dockernetworkactor.Ticket, error) {
	c.log.LowTrace("clerk: create network %q driver=%q", name, opts.Driver)
	return c.actor.Create(ctx, name, opts)
}

// Remove submits an async network removal.
func (c *Clerk) Remove(ctx context.Context, networkID string) (*dockernetworkactor.Ticket, error) {
	c.log.LowTrace("clerk: remove network %q", networkID)
	return c.actor.Remove(ctx, networkID)
}

// Prune submits an async prune of unused networks.
func (c *Clerk) Prune(ctx context.Context, pruneFilter dockerfilters.Args) (*dockernetworkactor.Ticket, error) {
	c.log.LowTrace("clerk: prune networks")
	return c.actor.Prune(ctx, pruneFilter)
}

// ── Read-only operations ──────────────────────────────────────────────────────

// List asynchronously lists networks.
func (c *Clerk) List(ctx context.Context, opts dockertypes.NetworkListOptions) <-chan dockernetworkactor.ListResult {
	return c.actor.List(ctx, opts)
}

// Inspect asynchronously inspects the named network.
func (c *Clerk) Inspect(ctx context.Context, networkID string, opts dockertypes.NetworkInspectOptions) <-chan dockernetworkactor.InspectResult {
	return c.actor.Inspect(ctx, networkID, opts)
}

// ── Store refresh ─────────────────────────────────────────────────────────────

// Refresh fetches the current network list from Docker, registers every network
// in the store (preserving in-band origin for known networks; assigning
// out-of-band to new arrivals), and reconciles to mark any previously-present
// network that has vanished as NetworkStatusDeletedOutOfBand.
func (c *Clerk) Refresh(ctx context.Context) error {
	c.log.LowTrace("clerk: refreshing network store")

	res := <-c.actor.List(ctx, dockertypes.NetworkListOptions{})
	if res.Err != nil {
		c.log.Error("clerk: refresh: list failed: %v", res.Err)
		return res.Err
	}

	liveIDs := make([]string, 0, len(res.Networks))
	for _, net := range res.Networks {
		liveIDs = append(liveIDs, net.ID)
		c.store.Register(dockernetworkstate.RegisterNetworkParams{
			DockerID:   net.ID,
			Name:       net.Name,
			Driver:     net.Driver,
			Scope:      net.Scope,
			Internal:   net.Internal,
			Attachable: net.Attachable,
			EnableIPv6: net.EnableIPv6,
			Origin:     dockernetworkstate.NetworkOriginOutOfBand,
		})
	}

	removed := c.store.Reconcile(liveIDs)
	c.log.Debug("clerk: refresh complete: live=%d reconciled_out=%d", len(liveIDs), len(removed))
	return nil
}
