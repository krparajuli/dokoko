package dockercontainerops

import (
	"context"
	"fmt"

	"dokoko.ai/dokoko/internal/docker"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"
	dockernetwork "github.com/docker/docker/api/types/network"
)

// Ops provides container-level operations against a Docker daemon.
type Ops struct {
	conn *docker.Connection
	log  *logger.Logger
}

// New constructs an Ops bound to an existing Connection.
func New(conn *docker.Connection, log *logger.Logger) *Ops {
	log.LowTrace("initialising container ops")
	log.Trace("container ops bound to connection %p", conn)
	return &Ops{conn: conn, log: log}
}

// Create creates a new container with the given name, config, host config, and
// optional network config.  Pass nil networkConfig to skip network attachment
// at creation time (containers can be connected later via Connect).
//
// Port bindings are specified via config.ExposedPorts (nat.PortSet) and
// hostConfig.PortBindings (nat.PortMap).  Volume mounts are specified via
// hostConfig.Binds ([]string bind-mount specs) or hostConfig.Mounts.
func (o *Ops) Create(
	ctx context.Context,
	name string,
	config *dockercontainer.Config,
	hostConfig *dockercontainer.HostConfig,
	networkConfig *dockernetwork.NetworkingConfig,
) (dockercontainer.CreateResponse, error) {
	o.log.LowTrace("creating container: name=%s image=%s", name, config.Image)
	o.log.Debug("create config: image=%s cmd=%v entrypoint=%v exposedPorts=%d",
		config.Image, config.Cmd, config.Entrypoint, len(config.ExposedPorts))

	if hostConfig != nil {
		o.log.Debug("create hostConfig: networkMode=%s autoRemove=%v portBindings=%d binds=%d mounts=%d",
			hostConfig.NetworkMode, hostConfig.AutoRemove,
			len(hostConfig.PortBindings), len(hostConfig.Binds), len(hostConfig.Mounts))
		for port, bindings := range hostConfig.PortBindings {
			for _, b := range bindings {
				o.log.Trace("  portBinding: %s → %s:%s", port, b.HostIP, b.HostPort)
			}
		}
		for i, bind := range hostConfig.Binds {
			o.log.Trace("  bind[%d]: %s", i, bind)
		}
		for i, m := range hostConfig.Mounts {
			o.log.Trace("  mount[%d]: type=%s src=%s target=%s ro=%v", i, m.Type, m.Source, m.Target, m.ReadOnly)
		}
	}

	if networkConfig != nil {
		o.log.Debug("create networkConfig: endpoints=%d", len(networkConfig.EndpointsConfig))
		for netName := range networkConfig.EndpointsConfig {
			o.log.Trace("  endpoint: %s", netName)
		}
	}

	resp, err := o.conn.Client().ContainerCreate(ctx, config, hostConfig, networkConfig, nil, name)
	if err != nil {
		o.log.Error("ContainerCreate failed for %q: %v", name, err)
		return dockercontainer.CreateResponse{}, fmt.Errorf("create container %q: %w", name, err)
	}

	o.log.Debug("container created: id=%s warnings=%d", shortID(resp.ID), len(resp.Warnings))
	for i, w := range resp.Warnings {
		o.log.Warn("create warning[%d]: %s", i, w)
	}

	o.log.Info("container created: name=%s id=%s", name, shortID(resp.ID))
	return resp, nil
}

// Start starts the container identified by containerID.
func (o *Ops) Start(ctx context.Context, containerID string, opts dockercontainer.StartOptions) error {
	o.log.LowTrace("starting container: %s", containerID)
	o.log.Debug("start options: checkpointID=%q checkpointDir=%q", opts.CheckpointID, opts.CheckpointDir)

	if err := o.conn.Client().ContainerStart(ctx, containerID, opts); err != nil {
		o.log.Error("ContainerStart failed for %q: %v", containerID, err)
		return fmt.Errorf("start container %q: %w", containerID, err)
	}

	o.log.Debug("start request accepted for %q", containerID)
	o.log.Info("container started: %s", containerID)
	return nil
}

// Stop stops the container identified by containerID.
func (o *Ops) Stop(ctx context.Context, containerID string, opts dockercontainer.StopOptions) error {
	o.log.LowTrace("stopping container: %s", containerID)
	if opts.Timeout != nil {
		o.log.Debug("stop options: timeout=%ds", *opts.Timeout)
	}

	if err := o.conn.Client().ContainerStop(ctx, containerID, opts); err != nil {
		o.log.Error("ContainerStop failed for %q: %v", containerID, err)
		return fmt.Errorf("stop container %q: %w", containerID, err)
	}

	o.log.Debug("stop request accepted for %q", containerID)
	o.log.Info("container stopped: %s", containerID)
	return nil
}

// Remove removes the container identified by containerID.
// Set opts.Force to remove a running container; set opts.RemoveVolumes to also
// remove anonymous volumes attached to the container.
func (o *Ops) Remove(ctx context.Context, containerID string, opts dockercontainer.RemoveOptions) error {
	o.log.LowTrace("removing container: %s", containerID)
	o.log.Debug("remove options: force=%v removeVolumes=%v removeLinks=%v", opts.Force, opts.RemoveVolumes, opts.RemoveLinks)

	if err := o.conn.Client().ContainerRemove(ctx, containerID, opts); err != nil {
		o.log.Error("ContainerRemove failed for %q: %v", containerID, err)
		return fmt.Errorf("remove container %q: %w", containerID, err)
	}

	o.log.Debug("remove request accepted for %q", containerID)
	o.log.Info("container removed: %s", containerID)
	return nil
}

// Connect attaches a running container to the network identified by networkID.
// Pass nil config to use default endpoint settings (no aliases, no static IP).
func (o *Ops) Connect(ctx context.Context, networkID, containerID string, config *dockernetwork.EndpointSettings) error {
	o.log.LowTrace("connecting container to network: container=%s network=%s", containerID, networkID)
	if config != nil {
		o.log.Debug("connect endpoint: aliases=%v ipv4=%s ipv6=%s",
			config.Aliases, config.IPAddress, config.GlobalIPv6Address)
	}

	if err := o.conn.Client().NetworkConnect(ctx, networkID, containerID, config); err != nil {
		o.log.Error("NetworkConnect failed (network=%q container=%q): %v", networkID, containerID, err)
		return fmt.Errorf("connect container %q to network %q: %w", containerID, networkID, err)
	}

	o.log.Debug("connect request accepted: container=%s network=%s", containerID, networkID)
	o.log.Info("container %s connected to network %s", containerID, networkID)
	return nil
}

// Disconnect detaches a container from the network identified by networkID.
// Set force=true to disconnect even if the container is not running.
func (o *Ops) Disconnect(ctx context.Context, networkID, containerID string, force bool) error {
	o.log.LowTrace("disconnecting container from network: container=%s network=%s", containerID, networkID)
	o.log.Debug("disconnect options: force=%v", force)

	if err := o.conn.Client().NetworkDisconnect(ctx, networkID, containerID, force); err != nil {
		o.log.Error("NetworkDisconnect failed (network=%q container=%q): %v", networkID, containerID, err)
		return fmt.Errorf("disconnect container %q from network %q: %w", containerID, networkID, err)
	}

	o.log.Debug("disconnect request accepted: container=%s network=%s", containerID, networkID)
	o.log.Info("container %s disconnected from network %s", containerID, networkID)
	return nil
}

// List returns a slice of container summaries. Pass a zero-value ListOptions
// to list running containers; set All=true to include stopped containers.
func (o *Ops) List(ctx context.Context, opts dockercontainer.ListOptions) ([]dockertypes.Container, error) {
	o.log.LowTrace("listing containers")
	o.log.Debug("list options: all=%v filters=%v limit=%d", opts.All, opts.Filters, opts.Limit)

	containers, err := o.conn.Client().ContainerList(ctx, opts)
	if err != nil {
		o.log.Error("ContainerList failed: %v", err)
		return nil, fmt.Errorf("container list: %w", err)
	}

	o.log.Debug("container list returned %d entries", len(containers))
	for i, c := range containers {
		o.log.Trace("container[%d]: id=%s names=%v image=%s status=%s ports=%d",
			i, shortID(c.ID), c.Names, c.Image, c.Status, len(c.Ports))
	}

	o.log.Info("listed %d containers", len(containers))
	return containers, nil
}

// Inspect returns detailed metadata for a single container by ID or name.
func (o *Ops) Inspect(ctx context.Context, containerID string) (dockertypes.ContainerJSON, error) {
	o.log.LowTrace("inspecting container: %s", containerID)

	resp, err := o.conn.Client().ContainerInspect(ctx, containerID)
	if err != nil {
		o.log.Error("ContainerInspect failed for %q: %v", containerID, err)
		return dockertypes.ContainerJSON{}, fmt.Errorf("inspect container %q: %w", containerID, err)
	}

	o.log.Debug("inspect response: id=%s name=%s image=%s running=%v",
		shortID(resp.ID), resp.Name, resp.Config.Image, resp.State.Running)
	o.log.Trace("inspect state: status=%s pid=%d exitCode=%d",
		resp.State.Status, resp.State.Pid, resp.State.ExitCode)
	if resp.HostConfig != nil {
		o.log.Trace("inspect hostConfig: networkMode=%s portBindings=%d binds=%d mounts=%d",
			resp.HostConfig.NetworkMode,
			len(resp.HostConfig.PortBindings), len(resp.HostConfig.Binds), len(resp.HostConfig.Mounts))
	}
	if resp.NetworkSettings != nil {
		o.log.Trace("inspect networks: %d attached", len(resp.NetworkSettings.Networks))
	}

	o.log.Info("inspected container %s (name=%s, running=%v)", shortID(resp.ID), resp.Name, resp.State.Running)
	return resp, nil
}

// Exists reports whether a container is present (by ID or name).
// It uses Inspect internally so the result is authoritative.
func (o *Ops) Exists(ctx context.Context, containerID string) (bool, error) {
	o.log.LowTrace("checking container existence: %s", containerID)

	_, err := o.conn.Client().ContainerInspect(ctx, containerID)
	if err != nil {
		if isNotFound(err) {
			o.log.Debug("container %q not found", containerID)
			return false, nil
		}
		o.log.Error("Exists check failed for %q: %v", containerID, err)
		return false, fmt.Errorf("exists check container %q: %w", containerID, err)
	}

	o.log.Debug("container %q found", containerID)
	o.log.Info("container %q exists", containerID)
	return true, nil
}

// shortID truncates a full container SHA256 ID to a readable 12-character prefix.
func shortID(id string) string {
	const prefix = "sha256:"
	s := id
	if len(s) > len(prefix) && s[:len(prefix)] == prefix {
		s = s[len(prefix):]
	}
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// isNotFound returns true when the Docker daemon signals a 404-style error.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, needle := range []string{"No such container", "not found", "404"} {
		for i := 0; i+len(needle) <= len(msg); i++ {
			if msg[i:i+len(needle)] == needle {
				return true
			}
		}
	}
	return false
}
