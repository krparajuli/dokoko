// Package webcontainersclerk is the public API facade for the web-container
// subsystem.  It wires the actor, state machine, session store, and env store
// together and exposes a simple interface to callers (the HTTP handlers).
package webcontainersclerk

import (
	"context"
	"errors"

	webcontainersactor "dokoko.ai/dokoko/internal/webcontainers/actor"
	webcontainerscatalog "dokoko.ai/dokoko/internal/webcontainers/catalog"
	webcontainersenvstore "dokoko.ai/dokoko/internal/webcontainers/envstore"
	webcontainersstate "dokoko.ai/dokoko/internal/webcontainers/state"
	"dokoko.ai/dokoko/pkg/logger"
)

// actorIface is the subset of *webcontainersactor.Actor used by Clerk.
type actorIface interface {
	Provision(ctx context.Context, userID string, def *webcontainerscatalog.ImageDef, envVars map[string]string) (*webcontainersactor.Ticket, error)
	Terminate(ctx context.Context, userID, containerName string) (*webcontainersactor.Ticket, error)
	Close()
}

// envApplier is satisfied by *webcontainersops.Ops.  It allows the clerk to
// push updated env vars into an already-running container without going
// through the async actor.
type envApplier interface {
	ApplyEnvVars(ctx context.Context, containerName string, envVars map[string]string) error
}

// Clerk is the public entry-point for the web-container subsystem.
// All methods are safe for concurrent use.
type Clerk struct {
	actor    actorIface
	ops      envApplier
	state    *webcontainersstate.State
	store    *webcontainersstate.Store
	envStore *webcontainersenvstore.Store
	log      *logger.Logger
}

// New wires the clerk together.
func New(
	actor actorIface,
	ops envApplier,
	state *webcontainersstate.State,
	store *webcontainersstate.Store,
	envStore *webcontainersenvstore.Store,
	log *logger.Logger,
) *Clerk {
	log.LowTrace("initialising webcontainer clerk")
	return &Clerk{actor: actor, ops: ops, state: state, store: store, envStore: envStore, log: log}
}

// Provision submits an async provision request for the given user and catalog
// image ID.  Stored env vars for the user are injected into the container.
// Returns a Ticket immediately; callers may poll GetSession to check when
// status transitions from "provisioning" to "ready" or "error".
func (c *Clerk) Provision(ctx context.Context, userID, catalogID string) (*webcontainersactor.Ticket, error) {
	def := webcontainerscatalog.Find(catalogID)
	if def == nil {
		return nil, &ErrUnknownCatalogID{ID: catalogID}
	}
	return c.actor.Provision(ctx, userID, def, c.envStore.Get(userID))
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

// ── Environment variables ─────────────────────────────────────────────────────

// GetEnvVars returns a copy of the stored env vars for userID.
// Returns nil when the user has no stored vars.
func (c *Clerk) GetEnvVars(userID string) map[string]string {
	return c.envStore.Get(userID)
}

// SetEnvVars atomically replaces all env vars for userID with vars, then
// applies them live to the running container (if one is ready).
// Passing a nil or empty map clears all stored vars.
func (c *Clerk) SetEnvVars(ctx context.Context, userID string, vars map[string]string) error {
	c.envStore.Replace(userID, vars)
	sess := c.store.GetSession(userID)
	if sess == nil || sess.Status != webcontainersstate.StatusReady || sess.ContainerName == "" {
		return nil // no running container — vars will be applied at next provision
	}
	if err := c.ops.ApplyEnvVars(ctx, sess.ContainerName, vars); err != nil {
		c.log.Warn("webcontainer clerk: ApplyEnvVars failed for %s: %v", userID, err)
		return err
	}
	return nil
}

// ── Internal accessors ────────────────────────────────────────────────────────

// Store returns the underlying session store.
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
