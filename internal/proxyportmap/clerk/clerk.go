// Package proxyportmapclerk is the public API facade for the proxyportmap
// subsystem.  It wires the actor, state, and store together.
package proxyportmapclerk

import (
	"context"
	"errors"

	proxyportmapactor "dokoko.ai/dokoko/internal/proxyportmap/actor"
	proxyportmapstate "dokoko.ai/dokoko/internal/proxyportmap/state"
	"dokoko.ai/dokoko/pkg/logger"
)

// ErrNoResult is returned when no scan result exists for the requested user.
var ErrNoResult = errors.New("proxyportmap: no scan result for user")

// Actor is the interface the Clerk uses for all port-map mutations.
type Actor interface {
	ScanAndMap(ctx context.Context, userID, containerName, containerID string) (*proxyportmapactor.Ticket, error)
	Unmap(ctx context.Context, userID string) (*proxyportmapactor.Ticket, error)
	Close()
}

// Clerk is the single entry point for the proxyportmap subsystem.
// All exported methods are safe for concurrent use.
type Clerk struct {
	actor Actor
	state *proxyportmapstate.State
	store *proxyportmapstate.Store
	log   *logger.Logger
}

// New returns a Clerk that wraps the given actor, state, and store.
func New(act Actor, st *proxyportmapstate.State, store *proxyportmapstate.Store, log *logger.Logger) *Clerk {
	log.LowTrace("creating proxyportmap clerk")
	return &Clerk{actor: act, state: st, store: store, log: log}
}

// ── Accessors ─────────────────────────────────────────────────────────────────

// State returns the operation State machine.
func (c *Clerk) State() *proxyportmapstate.State { return c.state }

// Store returns the scan-result Store.
func (c *Clerk) Store() *proxyportmapstate.Store { return c.store }

// ── Mutations ─────────────────────────────────────────────────────────────────

// ScanAndMap submits an async operation to scan containerName for listening
// TCP ports and register them with the nginx proxy.
func (c *Clerk) ScanAndMap(ctx context.Context, userID, containerName, containerID string) (*proxyportmapactor.Ticket, error) {
	c.log.LowTrace("proxyportmap clerk: ScanAndMap user=%s container=%s", userID, containerName)
	return c.actor.ScanAndMap(ctx, userID, containerName, containerID)
}

// Unmap submits an async operation to deregister all ports for userID from
// the nginx proxy and remove the stored scan result.
func (c *Clerk) Unmap(ctx context.Context, userID string) (*proxyportmapactor.Ticket, error) {
	c.log.LowTrace("proxyportmap clerk: Unmap user=%s", userID)
	return c.actor.Unmap(ctx, userID)
}

// ── Queries ───────────────────────────────────────────────────────────────────

// GetResult returns the latest scan result for userID, or nil if absent.
func (c *Clerk) GetResult(userID string) *proxyportmapstate.ScanResult {
	return c.store.GetResult(userID)
}
