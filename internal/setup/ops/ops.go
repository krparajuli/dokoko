// Package setupops provides the Docker operations needed during startup setup.
// It delegates container management to portproxyops so the proxy container
// lifecycle lives in one place.
package setupops

import (
	"context"

	portproxyops "dokoko.ai/dokoko/internal/portproxy/ops"
	"dokoko.ai/dokoko/pkg/logger"
)

// Ops provides Docker operations for startup setup tasks.
type Ops struct {
	pp  *portproxyops.Ops
	log *logger.Logger
}

// New returns an Ops that delegates proxy container management to pp.
func New(pp *portproxyops.Ops, log *logger.Logger) *Ops {
	log.LowTrace("initialising setup ops")
	return &Ops{pp: pp, log: log}
}

// EnsureProxyContainer pulls nginx:alpine if not present locally and starts
// the dokoko-proxy container.  Returns the container ID on success.
func (o *Ops) EnsureProxyContainer(ctx context.Context) (string, error) {
	o.log.LowTrace("setup ops: ensuring proxy container")
	return o.pp.EnsureProxyContainer(ctx)
}
