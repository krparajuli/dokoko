// Package dockervolumeclerk provides a Clerk that acts as a proxy between an
// outside manager and the Docker volume subsystem.
//
// The Clerk wires three existing layers together:
//
//   - Actor — async mutation dispatch (Create, Remove, Prune) and read-only queries
//   - State — in-flight mutation lifecycle (requested / active / failed / abandoned)
//   - Store — persistent volume inventory (Register, Reconcile, Mark*)
package dockervolumeclerk

import (
	"context"

	dockervolumeactor "dokoko.ai/dokoko/internal/docker/volumes/actor"
	dockervolumestate "dokoko.ai/dokoko/internal/docker/volumes/state"
	"dokoko.ai/dokoko/pkg/logger"
	dockerfilters "github.com/docker/docker/api/types/filters"
	dockervolume "github.com/docker/docker/api/types/volume"
)

// Actor is the interface the Clerk uses for all volume operations.
// *dockervolumeactor.Actor satisfies this interface; tests may inject a fake.
type Actor interface {
	Create(ctx context.Context, opts dockervolume.CreateOptions) (*dockervolumeactor.Ticket, error)
	Remove(ctx context.Context, name string, force bool) (*dockervolumeactor.Ticket, error)
	Prune(ctx context.Context, pruneFilter dockerfilters.Args) (*dockervolumeactor.Ticket, error)
	List(ctx context.Context, opts dockervolume.ListOptions) <-chan dockervolumeactor.ListResult
	Inspect(ctx context.Context, name string) <-chan dockervolumeactor.InspectResult
}

// Clerk is the single entry point for an outside manager that needs to
// interact with Docker volumes.  It composes the actor (mutations), state
// (lifecycle tracking), and store (volume inventory).
//
// All exported methods are safe for concurrent use.
type Clerk struct {
	actor Actor
	state *dockervolumestate.State
	store *dockervolumestate.VolumeStore
	log   *logger.Logger
}

// New returns a Clerk that wraps act, st, and store.
func New(act Actor, st *dockervolumestate.State, store *dockervolumestate.VolumeStore, log *logger.Logger) *Clerk {
	log.LowTrace("creating volume clerk")
	return &Clerk{actor: act, state: st, store: store, log: log}
}

// ── Accessors ─────────────────────────────────────────────────────────────────

// State returns the mutation-lifecycle State.
func (c *Clerk) State() *dockervolumestate.State { return c.state }

// Store returns the volume inventory VolumeStore.
func (c *Clerk) Store() *dockervolumestate.VolumeStore { return c.store }

// ── Mutations ─────────────────────────────────────────────────────────────────

// Create submits an async volume creation.
func (c *Clerk) Create(ctx context.Context, opts dockervolume.CreateOptions) (*dockervolumeactor.Ticket, error) {
	c.log.LowTrace("clerk: create volume %q", opts.Name)
	return c.actor.Create(ctx, opts)
}

// Remove submits an async volume removal.
func (c *Clerk) Remove(ctx context.Context, name string, force bool) (*dockervolumeactor.Ticket, error) {
	c.log.LowTrace("clerk: remove volume %q force=%v", name, force)
	return c.actor.Remove(ctx, name, force)
}

// Prune submits an async prune of unused volumes.
func (c *Clerk) Prune(ctx context.Context, pruneFilter dockerfilters.Args) (*dockervolumeactor.Ticket, error) {
	c.log.LowTrace("clerk: prune volumes")
	return c.actor.Prune(ctx, pruneFilter)
}

// ── Read-only operations ──────────────────────────────────────────────────────

// List asynchronously lists volumes.
func (c *Clerk) List(ctx context.Context, opts dockervolume.ListOptions) <-chan dockervolumeactor.ListResult {
	return c.actor.List(ctx, opts)
}

// Inspect asynchronously inspects the named volume.
func (c *Clerk) Inspect(ctx context.Context, name string) <-chan dockervolumeactor.InspectResult {
	return c.actor.Inspect(ctx, name)
}

// ── Store refresh ─────────────────────────────────────────────────────────────

// Refresh fetches the current volume list from Docker, registers every volume
// in the store (preserving in-band origin for known volumes; assigning
// out-of-band to new arrivals), and reconciles to mark any previously-present
// volume that has vanished as VolumeStatusDeletedOutOfBand.
func (c *Clerk) Refresh(ctx context.Context) error {
	c.log.LowTrace("clerk: refreshing volume store")

	res := <-c.actor.List(ctx, dockervolume.ListOptions{})
	if res.Err != nil {
		c.log.Error("clerk: refresh: list failed: %v", res.Err)
		return res.Err
	}

	liveNames := make([]string, 0, len(res.Response.Volumes))
	for _, vol := range res.Response.Volumes {
		liveNames = append(liveNames, vol.Name)
		c.store.Register(dockervolumestate.RegisterVolumeParams{
			Name:       vol.Name,
			Driver:     vol.Driver,
			Mountpoint: vol.Mountpoint,
			Scope:      vol.Scope,
			Labels:     vol.Labels,
			Options:    vol.Options,
			Origin:     dockervolumestate.VolumeOriginOutOfBand,
		})
	}

	removed := c.store.Reconcile(liveNames)
	c.log.Debug("clerk: refresh complete: live=%d reconciled_out=%d", len(liveNames), len(removed))
	return nil
}
