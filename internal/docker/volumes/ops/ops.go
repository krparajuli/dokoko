// Package dockervolumeops wraps the Docker daemon's volume API with structured
// logging.  Every method maps 1-to-1 to a Docker client call.
package dockervolumeops

import (
	"context"
	"fmt"

	"dokoko.ai/dokoko/internal/docker"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
	dockerfilters "github.com/docker/docker/api/types/filters"
	dockervolume "github.com/docker/docker/api/types/volume"
)

// Ops provides typed, logged wrappers around Docker volume operations.
type Ops struct {
	conn *docker.Connection
	log  *logger.Logger
}

// New returns a ready-to-use Ops using the supplied connection and logger.
func New(conn *docker.Connection, log *logger.Logger) *Ops {
	log.LowTrace("creating volume ops")
	log.Debug("volume ops allocated (conn=%p)", conn)
	return &Ops{conn: conn, log: log}
}

// Create creates a new Docker volume.  An empty Name in opts lets the daemon
// generate a unique name.
func (o *Ops) Create(ctx context.Context, opts dockervolume.CreateOptions) (dockervolume.Volume, error) {
	o.log.LowTrace("Create: name=%q driver=%q", opts.Name, opts.Driver)
	o.log.Debug("Create opts: labels=%d driverOpts=%d", len(opts.Labels), len(opts.DriverOpts))

	vol, err := o.conn.Client().VolumeCreate(ctx, opts)
	if err != nil {
		o.log.Error("Create failed: name=%q driver=%q: %v", opts.Name, opts.Driver, err)
		return dockervolume.Volume{}, err
	}

	o.log.Debug("Create succeeded: name=%q driver=%q mountpoint=%s", vol.Name, vol.Driver, vol.Mountpoint)
	o.log.Info("volume created: name=%q", vol.Name)
	return vol, nil
}

// List returns all volumes matching the provided options.
func (o *Ops) List(ctx context.Context, opts dockervolume.ListOptions) (dockervolume.ListResponse, error) {
	o.log.LowTrace("List: filters=%v", opts.Filters)

	resp, err := o.conn.Client().VolumeList(ctx, opts)
	if err != nil {
		o.log.Error("List failed: %v", err)
		return dockervolume.ListResponse{}, err
	}

	o.log.Debug("List: returned %d volumes, %d warnings", len(resp.Volumes), len(resp.Warnings))
	for _, w := range resp.Warnings {
		o.log.Warn("List warning: %s", w)
	}
	o.log.Info("volume list: %d volumes", len(resp.Volumes))
	return resp, nil
}

// Inspect returns detailed information about a single volume.
func (o *Ops) Inspect(ctx context.Context, name string) (dockervolume.Volume, error) {
	o.log.LowTrace("Inspect: name=%q", name)

	vol, err := o.conn.Client().VolumeInspect(ctx, name)
	if err != nil {
		o.log.Error("Inspect failed: name=%q: %v", name, err)
		return dockervolume.Volume{}, err
	}

	o.log.Debug("Inspect: name=%q driver=%q mountpoint=%s scope=%s", vol.Name, vol.Driver, vol.Mountpoint, vol.Scope)
	o.log.Info("volume inspected: name=%q driver=%q", vol.Name, vol.Driver)
	return vol, nil
}

// Remove deletes the named volume.  Set force to true to remove even if the
// volume is in use by a stopped container.
func (o *Ops) Remove(ctx context.Context, name string, force bool) error {
	o.log.LowTrace("Remove: name=%q force=%v", name, force)

	if err := o.conn.Client().VolumeRemove(ctx, name, force); err != nil {
		o.log.Error("Remove failed: name=%q force=%v: %v", name, force, err)
		return err
	}

	o.log.Debug("Remove succeeded: name=%q", name)
	o.log.Info("volume removed: name=%q", name)
	return nil
}

// Prune removes all unused volumes and returns a report of what was deleted.
func (o *Ops) Prune(ctx context.Context, pruneFilter dockerfilters.Args) (dockertypes.VolumesPruneReport, error) {
	o.log.LowTrace("Prune: filters=%v", pruneFilter)

	report, err := o.conn.Client().VolumesPrune(ctx, pruneFilter)
	if err != nil {
		o.log.Error("Prune failed: %v", err)
		return dockertypes.VolumesPruneReport{}, err
	}

	o.log.Debug("Prune: deleted=%v spaceReclaimed=%d", report.VolumesDeleted, report.SpaceReclaimed)
	o.log.Info("volumes pruned: count=%d spaceReclaimed=%s",
		len(report.VolumesDeleted), formatBytes(report.SpaceReclaimed))
	return report, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
