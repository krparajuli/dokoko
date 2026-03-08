// Package webcontainerops provides Docker SDK operations for web-container
// provisioning and termination.
package webcontainerops

import (
	"context"
	"fmt"
	"regexp"
	"strconv"

	"dokoko.ai/dokoko/internal/docker"
	webcontainerscatalog "dokoko.ai/dokoko/internal/webcontainers/catalog"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"
	dockernat "github.com/docker/go-connections/nat"
)

const (
	// ContainerPrefix is prepended to user IDs when naming containers.
	ContainerPrefix = "wc-"

	// TtydPort is the port ttyd listens on inside the container.
	TtydPort = "7681/tcp"

	// ManagedLabel marks containers as owned by the webcontainers subsystem.
	ManagedLabel      = "dokoko.webcontainer"
	ManagedLabelValue = "true"
)

// nonAlphanumRe strips characters that are invalid in Docker container names.
var nonAlphanumRe = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)

// SafeContainerName builds a Docker-safe container name from a user ID.
func SafeContainerName(userID string) string {
	safe := nonAlphanumRe.ReplaceAllString(userID, "_")
	return ContainerPrefix + safe
}

// Ops wraps Docker SDK calls for web-container lifecycle.
type Ops struct {
	conn *docker.Connection
	log  *logger.Logger
}

// New constructs an Ops bound to an existing Connection.
func New(conn *docker.Connection, log *logger.Logger) *Ops {
	log.LowTrace("initialising webcontainer ops")
	return &Ops{conn: conn, log: log}
}

// ProvisionResult carries the outcome of a successful Provision call.
type ProvisionResult struct {
	ContainerName string
	ContainerID   string
	HostPort      uint16
}

// ProvisionContainer ensures a web-container is running for the given user.
//
// If a container named "wc-<userID>" already exists and is running, its host
// port is read from Docker's port bindings and returned without recreating.
// If it exists but is stopped, it is removed and recreated.  Otherwise a
// fresh container is created from def.Image with the catalogue's startup
// script and a random host-port binding on 7681/tcp.
func (o *Ops) ProvisionContainer(ctx context.Context, userID string, def *webcontainerscatalog.ImageDef) (*ProvisionResult, error) {
	name := SafeContainerName(userID)
	o.log.Debug("webcontainer ops: provision name=%s image=%s", name, def.Image)

	cli := o.conn.Client()

	// Check whether the container already exists.
	existing, err := cli.ContainerInspect(ctx, name)
	if err == nil {
		// Container exists — if running, return its assigned port.
		if existing.State != nil && existing.State.Running {
			hostPort, pErr := hostPortFromInspect(existing)
			if pErr != nil {
				return nil, fmt.Errorf("provision: container running but port lookup failed: %w", pErr)
			}
			o.log.Info("webcontainer ops: reusing running container %s port=%d", name, hostPort)
			return &ProvisionResult{
				ContainerName: name,
				ContainerID:   existing.ID,
				HostPort:      hostPort,
			}, nil
		}

		// Exists but stopped — remove before recreating.
		o.log.Debug("webcontainer ops: container %s stopped, removing before recreate", name)
		_ = cli.ContainerRemove(ctx, name, dockercontainer.RemoveOptions{Force: true})
	}

	// ttyd base path so the reverse proxy can forward requests without rewriting.
	basePath := "/api/webcontainers/terminal/" + userID + "/"

	// Create the container.
	containerCfg := &dockercontainer.Config{
		Image:        def.Image,
		Cmd:          []string{"sh", "-c", def.StartScript},
		ExposedPorts: dockernat.PortSet{TtydPort: {}},
		Env:          []string{"TTYD_BASE_PATH=" + basePath},
		Labels: map[string]string{
			ManagedLabel:  ManagedLabelValue,
			"dokoko.user": userID,
		},
	}
	// Bind to 127.0.0.1 so ttyd is only reachable via the Go reverse proxy,
	// not directly from the outside world.
	hostCfg := &dockercontainer.HostConfig{
		PortBindings: dockernat.PortMap{
			TtydPort: []dockernat.PortBinding{{HostIP: "127.0.0.1", HostPort: "0"}},
		},
		RestartPolicy: dockercontainer.RestartPolicy{Name: "unless-stopped"},
	}

	resp, err := cli.ContainerCreate(ctx, containerCfg, hostCfg, nil, nil, name)
	if err != nil {
		return nil, fmt.Errorf("provision: ContainerCreate: %w", err)
	}
	o.log.Debug("webcontainer ops: created container %s id=%s", name, resp.ID)

	// Start it.
	if err := cli.ContainerStart(ctx, resp.ID, dockercontainer.StartOptions{}); err != nil {
		_ = cli.ContainerRemove(ctx, resp.ID, dockercontainer.RemoveOptions{Force: true})
		return nil, fmt.Errorf("provision: ContainerStart: %w", err)
	}

	// Inspect to discover the assigned host port.
	updated, err := cli.ContainerInspect(ctx, resp.ID)
	if err != nil {
		return nil, fmt.Errorf("provision: post-start inspect: %w", err)
	}

	hostPort, err := hostPortFromInspect(updated)
	if err != nil {
		return nil, fmt.Errorf("provision: post-start port lookup: %w", err)
	}

	o.log.Info("webcontainer ops: provisioned %s id=%s port=%d", name, resp.ID, hostPort)
	return &ProvisionResult{
		ContainerName: name,
		ContainerID:   resp.ID,
		HostPort:      hostPort,
	}, nil
}

// TerminateContainer stops and removes the named container.
func (o *Ops) TerminateContainer(ctx context.Context, containerName string) error {
	o.log.Debug("webcontainer ops: terminating %s", containerName)
	cli := o.conn.Client()

	timeout := 10
	if err := cli.ContainerStop(ctx, containerName, dockercontainer.StopOptions{Timeout: &timeout}); err != nil {
		o.log.Warn("webcontainer ops: stop %s: %v (continuing with remove)", containerName, err)
	}
	if err := cli.ContainerRemove(ctx, containerName, dockercontainer.RemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("terminate: ContainerRemove: %w", err)
	}

	o.log.Info("webcontainer ops: container %s terminated", containerName)
	return nil
}

// hostPortFromInspect extracts the host-side port mapped to TtydPort from an
// inspect response.
func hostPortFromInspect(info dockertypes.ContainerJSON) (uint16, error) {
	if info.NetworkSettings == nil {
		return 0, fmt.Errorf("NetworkSettings is nil")
	}
	bindings := info.NetworkSettings.Ports[dockernat.Port(TtydPort)]
	if len(bindings) == 0 {
		return 0, fmt.Errorf("no port binding found for %s", TtydPort)
	}
	p, err := strconv.ParseUint(bindings[0].HostPort, 10, 16)
	if err != nil {
		return 0, fmt.Errorf("invalid host port %q: %w", bindings[0].HostPort, err)
	}
	return uint16(p), nil
}
