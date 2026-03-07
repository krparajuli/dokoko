// Package dockernetworkops wraps the Docker daemon's network API with structured
// logging.  Every method maps 1-to-1 to a Docker client call.
package dockernetworkops

import (
	"context"

	"dokoko.ai/dokoko/internal/docker"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
	dockerfilters "github.com/docker/docker/api/types/filters"
)

// Ops provides typed, logged wrappers around Docker network operations.
type Ops struct {
	conn *docker.Connection
	log  *logger.Logger
}

// New returns a ready-to-use Ops using the supplied connection and logger.
func New(conn *docker.Connection, log *logger.Logger) *Ops {
	log.LowTrace("creating network ops")
	log.Debug("network ops allocated (conn=%p)", conn)
	return &Ops{conn: conn, log: log}
}

// Create creates a new Docker network with the given name and options.
func (o *Ops) Create(ctx context.Context, name string, opts dockertypes.NetworkCreate) (dockertypes.NetworkCreateResponse, error) {
	o.log.LowTrace("Create: name=%q driver=%q", name, opts.Driver)
	o.log.Debug("Create opts: labels=%d internal=%v attachable=%v", len(opts.Labels), opts.Internal, opts.Attachable)

	resp, err := o.conn.Client().NetworkCreate(ctx, name, opts)
	if err != nil {
		o.log.Error("Create failed: name=%q driver=%q: %v", name, opts.Driver, err)
		return dockertypes.NetworkCreateResponse{}, err
	}

	o.log.Debug("Create succeeded: name=%q id=%s warning=%q", name, resp.ID, resp.Warning)
	o.log.Info("network created: name=%q id=%s", name, resp.ID)
	return resp, nil
}

// List returns all networks matching the provided options.
func (o *Ops) List(ctx context.Context, opts dockertypes.NetworkListOptions) ([]dockertypes.NetworkResource, error) {
	o.log.LowTrace("List: filters=%v", opts.Filters)

	networks, err := o.conn.Client().NetworkList(ctx, opts)
	if err != nil {
		o.log.Error("List failed: %v", err)
		return nil, err
	}

	o.log.Debug("List: returned %d networks", len(networks))
	o.log.Info("network list: %d networks", len(networks))
	return networks, nil
}

// Inspect returns detailed information about a single network.
func (o *Ops) Inspect(ctx context.Context, networkID string, opts dockertypes.NetworkInspectOptions) (dockertypes.NetworkResource, error) {
	o.log.LowTrace("Inspect: networkID=%q", networkID)

	net, err := o.conn.Client().NetworkInspect(ctx, networkID, opts)
	if err != nil {
		o.log.Error("Inspect failed: networkID=%q: %v", networkID, err)
		return dockertypes.NetworkResource{}, err
	}

	o.log.Debug("Inspect: id=%q name=%q driver=%q scope=%s", net.ID, net.Name, net.Driver, net.Scope)
	o.log.Info("network inspected: name=%q driver=%q", net.Name, net.Driver)
	return net, nil
}

// Remove deletes the network with the given ID or name.
func (o *Ops) Remove(ctx context.Context, networkID string) error {
	o.log.LowTrace("Remove: networkID=%q", networkID)

	if err := o.conn.Client().NetworkRemove(ctx, networkID); err != nil {
		o.log.Error("Remove failed: networkID=%q: %v", networkID, err)
		return err
	}

	o.log.Debug("Remove succeeded: networkID=%q", networkID)
	o.log.Info("network removed: networkID=%q", networkID)
	return nil
}

// Prune removes all unused networks and returns a report of what was deleted.
func (o *Ops) Prune(ctx context.Context, pruneFilter dockerfilters.Args) (dockertypes.NetworksPruneReport, error) {
	o.log.LowTrace("Prune: filters=%v", pruneFilter)

	report, err := o.conn.Client().NetworksPrune(ctx, pruneFilter)
	if err != nil {
		o.log.Error("Prune failed: %v", err)
		return dockertypes.NetworksPruneReport{}, err
	}

	o.log.Debug("Prune: deleted=%v", report.NetworksDeleted)
	o.log.Info("networks pruned: count=%d", len(report.NetworksDeleted))
	return report, nil
}
