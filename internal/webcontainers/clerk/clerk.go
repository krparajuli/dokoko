// Package webcontainersclerk is the public API facade for the web-container
// subsystem.  It wires the actor, state machine, and session store together
// and exposes a simple interface to callers (the HTTP handlers).
package webcontainersclerk

import (
	"context"
	"errors"

	webcontainersactor "dokoko.ai/dokoko/internal/webcontainers/actor"
	webcontainerscatalog "dokoko.ai/dokoko/internal/webcontainers/catalog"
	webcontainersstate "dokoko.ai/dokoko/internal/webcontainers/state"
	"dokoko.ai/dokoko/pkg/logger"
)

// actorIface is the subset of *webcontainersactor.Actor used by Clerk.
type actorIface interface {
	Provision(ctx context.Context, userID string, def *webcontainerscatalog.ImageDef) (*webcontainersactor.Ticket, error)
	Terminate(ctx context.Context, userID, containerName string) (*webcontainersactor.Ticket, error)
	Close()
}

// Clerk is the public entry-point for the web-container subsystem.
// All methods are safe for concurrent use.
type Clerk struct {
	actor actorIface
	state *webcontainersstate.State
	store *webcontainersstate.Store
	log   *logger.Logger
}

// New wires the clerk together.
func New(actor actorIface, state *webcontainersstate.State, store *webcontainersstate.Store, log *logger.Logger) *Clerk {
	log.LowTrace("initialising webcontainer clerk")
	return &Clerk{actor: actor, state: state, store: store, log: log}
}

// Provision submits an async provision request for the given user and catalog
// image ID.  Returns a Ticket immediately; callers may poll GetSession to
// check when status transitions from "provisioning" to "ready" or "error".
func (c *Clerk) Provision(ctx context.Context, userID, catalogID string) (*webcontainersactor.Ticket, error) {
	def := webcontainerscatalog.Find(catalogID)
	if def == nil {
		return nil, &ErrUnknownCatalogID{ID: catalogID}
	}
	return c.actor.Provision(ctx, userID, def)
}

// Terminate submits an async termination for the given user's container.
// Returns ErrNoSession if the user has no active session.
func (c *Clerk) Terminate(ctx context.Context, userID string) (*webcontainersactor.Ticket, error) {
	sess := c.store.GetSession(userID)
	if sess == nil {
		return nil, ErrNoSession
	}
	return c.actor.Terminate(ctx, userID, sess.ContainerName)
}

// GetSession returns a copy of the user's current session, or nil if none.
func (c *Clerk) GetSession(userID string) *webcontainersstate.UserSession {
	return c.store.GetSession(userID)
}

// Catalog returns the ordered list of approved images.
func (c *Clerk) Catalog() []*webcontainerscatalog.ImageDef {
	return webcontainerscatalog.Catalog
}

// Store returns the underlying session store (for the HTTP handler interface).
func (c *Clerk) Store() *webcontainersstate.Store {
	return c.store
}

// State returns the underlying state machine.
func (c *Clerk) State() *webcontainersstate.State {
	return c.state
}

// Close shuts down the actor gracefully.
func (c *Clerk) Close() { c.actor.Close() }

// ── Errors ────────────────────────────────────────────────────────────────────

// ErrNoSession is returned when Terminate is called for a user with no session.
var ErrNoSession = errors.New("webcontainer: no session found for user")

// ErrUnknownCatalogID is returned when Provision receives an unrecognised catalog ID.
type ErrUnknownCatalogID struct{ ID string }

func (e *ErrUnknownCatalogID) Error() string {
	return "webcontainer: unknown catalog ID: " + e.ID
}
