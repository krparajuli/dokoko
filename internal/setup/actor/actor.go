// Package setupactor runs the one-shot startup setup tasks in a background
// goroutine and records the outcome in State.
package setupactor

import (
	"context"

	setupops "dokoko.ai/dokoko/internal/setup/ops"
	setupstate "dokoko.ai/dokoko/internal/setup/state"
	"dokoko.ai/dokoko/pkg/logger"
)

// Actor runs the setup tasks.  Call Run once; results are reflected in State.
type Actor struct {
	ops   *setupops.Ops
	state *setupstate.State
	log   *logger.Logger
}

// New creates an Actor.  Call Run to start the setup goroutine.
func New(ops *setupops.Ops, st *setupstate.State, log *logger.Logger) *Actor {
	return &Actor{ops: ops, state: st, log: log}
}

// Run starts setup in a background goroutine.  Call only once.
func (a *Actor) Run(ctx context.Context) {
	go a.run(ctx)
}

func (a *Actor) run(ctx context.Context) {
	a.log.Info("setup: starting")
	a.state.SetRunning()

	// Ensure the nginx:alpine image is present and dokoko-proxy is running.
	// This is a blocking call that may pull the image if it is not cached.
	a.log.Info("setup: ensuring proxy container (dokoko-proxy)")
	if _, err := a.ops.EnsureProxyContainer(ctx); err != nil {
		a.log.Error("setup: proxy container failed: %v", err)
		a.state.SetFailed(err)
		return
	}
	a.log.Info("setup: proxy container ready")

	a.state.SetDone()
	a.log.Info("setup: complete — server may now accept requests")
}
