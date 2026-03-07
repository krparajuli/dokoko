// Package dockerimageclerk provides a Clerk that acts as a proxy between an
// outside manager and the Docker image subsystem.
//
// The Clerk wires three existing layers together:
//
//   - Actor — async mutation dispatch (Pull, Remove, Tag) and read-only queries
//   - State — in-flight mutation lifecycle (requested / active / failed / abandoned)
//   - Store — persistent image inventory (Register, Reconcile, Mark*)
//
// Typical usage:
//
//	clerk := dockerimageclerk.New(actor, state, store, log)
//	ticket, err := clerk.Pull(ctx, "ubuntu:22.04", dockerimage.PullOptions{})
//	// ... wait for ticket to settle ...
//	err = clerk.Refresh(ctx)  // syncs store with live Docker inventory
package dockerimageclerk

import (
	"context"
	"time"

	dockerimageactor "dokoko.ai/dokoko/internal/docker/images/actor"
	dockerimagestate "dokoko.ai/dokoko/internal/docker/images/state"
	"dokoko.ai/dokoko/pkg/logger"
	dockerimage "github.com/docker/docker/api/types/image"
)

// Actor is the interface the Clerk uses for all image operations.
// *dockerimageactor.Actor satisfies this interface; tests may inject a fake.
type Actor interface {
	Pull(ctx context.Context, ref string, opts dockerimage.PullOptions) (*dockerimageactor.Ticket, error)
	Remove(ctx context.Context, imageID string, opts dockerimage.RemoveOptions) (*dockerimageactor.Ticket, error)
	Tag(ctx context.Context, source, target string) (*dockerimageactor.Ticket, error)
	List(ctx context.Context, opts dockerimage.ListOptions) <-chan dockerimageactor.ListResult
	Inspect(ctx context.Context, imageID string) <-chan dockerimageactor.InspectResult
	Exists(ctx context.Context, ref string) <-chan dockerimageactor.ExistsResult
}

// Clerk is the single entry point for an outside manager that needs to
// interact with Docker images.  It composes the actor (mutations), state
// (lifecycle tracking), and store (image inventory).
//
// All exported methods are safe for concurrent use.
type Clerk struct {
	actor Actor
	state *dockerimagestate.State
	store *dockerimagestate.Store
	log   *logger.Logger
}

// New returns a Clerk that wraps act, st, and store.
func New(act Actor, st *dockerimagestate.State, store *dockerimagestate.Store, log *logger.Logger) *Clerk {
	log.LowTrace("creating image clerk")
	return &Clerk{actor: act, state: st, store: store, log: log}
}

// ── Accessors ─────────────────────────────────────────────────────────────────

// State returns the mutation-lifecycle State.
func (c *Clerk) State() *dockerimagestate.State { return c.state }

// Store returns the image inventory Store.
func (c *Clerk) Store() *dockerimagestate.Store { return c.store }

// ── Mutations ─────────────────────────────────────────────────────────────────

// Pull submits an async pull for ref.  State is updated to active (with the
// daemon-reported image ID) on success, or failed on error.
func (c *Clerk) Pull(ctx context.Context, ref string, opts dockerimage.PullOptions) (*dockerimageactor.Ticket, error) {
	c.log.LowTrace("clerk: pull %q", ref)
	return c.actor.Pull(ctx, ref, opts)
}

// Remove submits an async removal of imageID.
func (c *Clerk) Remove(ctx context.Context, imageID string, opts dockerimage.RemoveOptions) (*dockerimageactor.Ticket, error) {
	c.log.LowTrace("clerk: remove %q", imageID)
	return c.actor.Remove(ctx, imageID, opts)
}

// Tag submits an async tag of source → target.
func (c *Clerk) Tag(ctx context.Context, source, target string) (*dockerimageactor.Ticket, error) {
	c.log.LowTrace("clerk: tag %q → %q", source, target)
	return c.actor.Tag(ctx, source, target)
}

// ── Read-only operations ──────────────────────────────────────────────────────

// List asynchronously lists images and returns a buffered result channel.
func (c *Clerk) List(ctx context.Context, opts dockerimage.ListOptions) <-chan dockerimageactor.ListResult {
	return c.actor.List(ctx, opts)
}

// Inspect asynchronously inspects imageID and returns a buffered result channel.
func (c *Clerk) Inspect(ctx context.Context, imageID string) <-chan dockerimageactor.InspectResult {
	return c.actor.Inspect(ctx, imageID)
}

// Exists asynchronously checks whether ref is present in the local image store.
func (c *Clerk) Exists(ctx context.Context, ref string) <-chan dockerimageactor.ExistsResult {
	return c.actor.Exists(ctx, ref)
}

// ── Store refresh ─────────────────────────────────────────────────────────────

// Refresh fetches the current image list from Docker, registers every image in
// the store (preserving in-band origin for known images; assigning out-of-band
// to new arrivals), and reconciles to mark any previously-present image that
// has vanished as ImageStatusDeletedOutOfBand.
//
// Callers should invoke Refresh periodically or after a known mutation settles.
func (c *Clerk) Refresh(ctx context.Context) error {
	c.log.LowTrace("clerk: refreshing image store")

	res := <-c.actor.List(ctx, dockerimage.ListOptions{All: true})
	if res.Err != nil {
		c.log.Error("clerk: refresh: list failed: %v", res.Err)
		return res.Err
	}

	liveIDs := make([]string, 0, len(res.Images))
	for _, img := range res.Images {
		liveIDs = append(liveIDs, img.ID)
		c.store.Register(dockerimagestate.RegisterParams{
			DockerID:       img.ID,
			RepoTags:       img.RepoTags,
			RepoDigests:    img.RepoDigests,
			Size:           img.Size,
			ImageCreatedAt: time.Unix(img.Created, 0),
			Origin:         dockerimagestate.OriginOutOfBand,
		})
	}

	removed := c.store.Reconcile(liveIDs)
	c.log.Debug("clerk: refresh complete: live=%d reconciled_out=%d", len(liveIDs), len(removed))
	return nil
}
