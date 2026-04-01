// Package setupregister is the public API for the setup service.
package setupregister

import (
	"context"

	setupactor "dokoko.ai/dokoko/internal/setup/actor"
	setupstate "dokoko.ai/dokoko/internal/setup/state"
)

// Register is the public interface for the setup service.
type Register struct {
	actor *setupactor.Actor
	state *setupstate.State
}

// New creates a Register wired to actor and state.
func New(actor *setupactor.Actor, state *setupstate.State) *Register {
	return &Register{actor: actor, state: state}
}

// Run starts setup in the background.  Call only once.
func (r *Register) Run(ctx context.Context) {
	r.actor.Run(ctx)
}

// Wait blocks until setup reaches PhaseDone or PhaseFailed, or ctx expires.
// Returns nil on success, the setup error on failure, or ctx.Err() on timeout.
func (r *Register) Wait(ctx context.Context) error {
	select {
	case <-r.state.Done():
		return r.state.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Phase returns the current setup phase.
func (r *Register) Phase() setupstate.Phase {
	return r.state.Phase()
}
