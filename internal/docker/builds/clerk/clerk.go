// Package dockerbuildclerk provides a Clerk that acts as a proxy between an
// outside manager and the Docker image build subsystem.
//
// The Clerk wires three existing layers together:
//
//   - Actor     — async build dispatch (Build, PruneCache)
//   - State     — in-flight mutation lifecycle (requested / active / failed / abandoned)
//   - BuildStore — persistent build event log (RegisterBuild, MarkSucceeded, …)
//
// Unlike the image/volume/network clerks the build clerk does not have a
// Refresh method because builds are one-shot events, not persistent resources.
// Instead, the clerk automatically bridges the actor's ticket lifecycle to the
// build store: each Build call spawns a lightweight watcher goroutine that
// settles the store record once the ticket closes.
//
// Call Close to drain all pending watchers before discarding the Clerk.
package dockerbuildclerk

import (
	"context"
	"sync"

	dockerbuildactor "dokoko.ai/dokoko/internal/docker/builds/actor"
	dockerbuildops "dokoko.ai/dokoko/internal/docker/builds/ops"
	dockerbuildstate "dokoko.ai/dokoko/internal/docker/builds/state"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
)

// Actor is the interface the Clerk uses for build operations.
// *dockerbuildactor.Actor satisfies this interface; tests may inject a fake.
type Actor interface {
	Build(ctx context.Context, req dockerbuildops.BuildRequest) (*dockerbuildactor.Ticket, error)
	PruneCache(ctx context.Context, opts dockertypes.BuildCachePruneOptions) (*dockerbuildactor.Ticket, error)
}

// Clerk is the single entry point for an outside manager that needs to
// build Docker images.  It composes the actor (async dispatch), state
// (mutation lifecycle), and build store (event log).
//
// All exported methods are safe for concurrent use.
type Clerk struct {
	actor      Actor
	state      *dockerbuildstate.State
	buildStore *dockerbuildstate.BuildStore
	log        *logger.Logger

	// wg tracks in-flight watcher goroutines so Close can drain them.
	wg sync.WaitGroup
}

// New returns a Clerk that wraps act, st, and store.
func New(act Actor, st *dockerbuildstate.State, store *dockerbuildstate.BuildStore, log *logger.Logger) *Clerk {
	log.LowTrace("creating build clerk")
	return &Clerk{actor: act, state: st, buildStore: store, log: log}
}

// ── Accessors ─────────────────────────────────────────────────────────────────

// State returns the mutation-lifecycle State.
func (c *Clerk) State() *dockerbuildstate.State { return c.state }

// BuildStore returns the build event log.
func (c *Clerk) BuildStore() *dockerbuildstate.BuildStore { return c.buildStore }

// ── Operations ────────────────────────────────────────────────────────────────

// Build submits an async image build.  It registers the build in the BuildStore
// immediately (status=Pending) and spawns a background watcher that transitions
// the record to Succeeded, Failed, or Abandoned once the ticket settles.
func (c *Clerk) Build(ctx context.Context, req dockerbuildops.BuildRequest) (*dockerbuildactor.Ticket, error) {
	c.log.LowTrace("clerk: build tags=%v", req.Tags)

	ticket, err := c.actor.Build(ctx, req)
	if err != nil {
		return nil, err
	}

	// Determine the context source for the store record.
	ctxDir := req.ContextDir
	if ctxDir == "" {
		ctxDir = req.RemoteContext
	}

	c.buildStore.RegisterBuild(dockerbuildstate.RegisterBuildParams{
		ChangeID:   ticket.ChangeID,
		Tags:       req.Tags,
		Dockerfile: req.Dockerfile,
		ContextDir: ctxDir,
		Platform:   req.Platform,
	})

	c.wg.Add(1)
	go c.watchBuild(ticket.ChangeID, ticket.Done)

	return ticket, nil
}

// PruneCache submits an async build cache prune.  The prune operation is not
// tracked in the build store (it produces no artifact).
func (c *Clerk) PruneCache(ctx context.Context, opts dockertypes.BuildCachePruneOptions) (*dockerbuildactor.Ticket, error) {
	c.log.LowTrace("clerk: prune cache")
	return c.actor.PruneCache(ctx, opts)
}

// Close waits for all in-flight build watcher goroutines to finish.
// It must be called before discarding the Clerk to avoid goroutine leaks.
func (c *Clerk) Close() {
	c.log.LowTrace("clerk: closing — waiting for build watchers")
	c.wg.Wait()
	c.log.Debug("clerk: all build watchers finished")
}

// ── Internal ──────────────────────────────────────────────────────────────────

// watchBuild blocks until done is closed, then reads the change outcome from
// the state machine and updates the build store accordingly.
func (c *Clerk) watchBuild(changeID string, done <-chan struct{}) {
	defer c.wg.Done()

	// Block until the ticket settles (actor always closes done).
	<-done

	status, rec, err := c.state.FindByID(changeID)
	if err != nil {
		c.log.Error("clerk: watchBuild: FindByID %s: %v", changeID, err)
		return
	}

	switch status {
	case dockerbuildstate.StatusActive:
		imageID := rec.(*dockerbuildstate.ActiveRecord).ImageID
		if markErr := c.buildStore.MarkSucceeded(changeID, imageID); markErr != nil {
			c.log.Error("clerk: MarkSucceeded %s: %v", changeID, markErr)
		}
	case dockerbuildstate.StatusFailed:
		errMsg := rec.(*dockerbuildstate.FailedRecord).Err
		if markErr := c.buildStore.MarkFailed(changeID, errMsg); markErr != nil {
			c.log.Error("clerk: MarkFailed %s: %v", changeID, markErr)
		}
	case dockerbuildstate.StatusAbandoned:
		reason := rec.(*dockerbuildstate.AbandonedRecord).Reason
		if markErr := c.buildStore.MarkAbandoned(changeID, reason); markErr != nil {
			c.log.Error("clerk: MarkAbandoned %s: %v", changeID, markErr)
		}
	default:
		c.log.Warn("clerk: watchBuild: unexpected status %q for change %s", status, changeID)
	}
}
