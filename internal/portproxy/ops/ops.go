// Package portproxyops provides Docker SDK operations for the dokoko-proxy
// nginx container.  It is a thin wrapper with no state — callers are
// responsible for sequencing calls correctly.
package portproxyops

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"time"

	"dokoko.ai/dokoko/internal/docker"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"
	dockerfilters "github.com/docker/docker/api/types/filters"
	dockerimage "github.com/docker/docker/api/types/image"
	dockermount "github.com/docker/docker/api/types/mount"
	dockernetwork "github.com/docker/docker/api/types/network"
	dockernat "github.com/docker/go-connections/nat"
)

// ── Constants ─────────────────────────────────────────────────────────────────

const (
	// ProxyContainerName is the well-known name of the managed nginx container.
	ProxyContainerName = "dokoko-proxy"

	// ProxyLabel is the Docker label key applied to all proxy-owned resources.
	ProxyLabel = "dokoko.portproxy"

	// ProxyLabelValue is the value of ProxyLabel.
	ProxyLabelValue = "true"

	// ConfigVolumeName is the Docker volume that holds the nginx config.
	ConfigVolumeName = "dokoko_proxy_config"

	// ProxyImage is the container image used for the proxy.
	ProxyImage = "nginx:alpine"
)

// proxyStartScript generates a self-contained OpenSSL cert and writes a
// minimal proxy.conf before exec-ing nginx in the foreground.
// proxyStartScript always writes a minimal valid nginx config on each start so
// that a stale or corrupt config on the persistent volume cannot cause a
// restart loop.  The TLS cert is still guarded (expensive to regenerate).
const proxyStartScript = `mkdir -p /etc/nginx/ssl && ` +
	`[ -f /etc/nginx/ssl/cert.pem ] || openssl req -x509 -nodes -days 3650 -newkey rsa:2048 ` +
	`-keyout /etc/nginx/ssl/key.pem -out /etc/nginx/ssl/cert.pem ` +
	`-subj '/CN=dokoko-proxy' 2>/dev/null && ` +
	`printf 'map $http_upgrade $connection_upgrade{default upgrade;'"'"''"'"' close;}\n' ` +
	`> /etc/nginx/conf.d/proxy.conf && ` +
	`exec nginx -g 'daemon off;'`

// ── Ops ───────────────────────────────────────────────────────────────────────

// Ops wraps a Docker connection and exposes port-proxy-specific operations.
type Ops struct {
	conn *docker.Connection
	log  *logger.Logger
}

// New constructs an Ops bound to an existing Connection.
func New(conn *docker.Connection, log *logger.Logger) *Ops {
	log.LowTrace("initialising portproxy ops")
	return &Ops{conn: conn, log: log}
}

// EnsureProxyContainer guarantees the dokoko-proxy container exists and is
// running.  If it is already running the existing container ID is returned.
// Otherwise the container is created and started.
func (o *Ops) EnsureProxyContainer(ctx context.Context) (string, error) {
	o.log.LowTrace("portproxy ops: ensuring proxy container")

	// Check if already running.
	info, err := o.conn.Client().ContainerInspect(ctx, ProxyContainerName)
	if err == nil {
		if info.State != nil && info.State.Running {
			o.log.Debug("portproxy ops: proxy container already running: id=%s", info.ID[:12])
			return info.ID, nil
		}
		o.log.Debug("portproxy ops: proxy container exists but not running, recreating")
		// Remove the stopped container so we can recreate it cleanly.
		_ = o.conn.Client().ContainerRemove(ctx, info.ID, dockercontainer.RemoveOptions{Force: true})
	}

	// Build port bindings for 8100-8199.
	exposedPorts := dockernat.PortSet{}
	portBindings := dockernat.PortMap{}
	for p := 8100; p <= 8199; p++ {
		port := dockernat.Port(fmt.Sprintf("%d/tcp", p))
		exposedPorts[port] = struct{}{}
		portBindings[port] = []dockernat.PortBinding{{HostPort: fmt.Sprintf("%d", p)}}
	}

	cfg := &dockercontainer.Config{
		Image:        ProxyImage,
		Cmd:          []string{"sh", "-c", proxyStartScript},
		Labels:       map[string]string{ProxyLabel: ProxyLabelValue},
		ExposedPorts: exposedPorts,
	}

	hostCfg := &dockercontainer.HostConfig{
		PortBindings:  portBindings,
		RestartPolicy: dockercontainer.RestartPolicy{Name: "unless-stopped"},
		Mounts: []dockermount.Mount{
			{
				Type:   dockermount.TypeVolume,
				Source: ConfigVolumeName,
				Target: "/etc/nginx/conf.d",
			},
		},
	}

	// Ensure the nginx:alpine image is available locally before creating.
	if err := o.ensureImage(ctx, ProxyImage); err != nil {
		return "", fmt.Errorf("portproxy: pull proxy image: %w", err)
	}

	o.log.Debug("portproxy ops: creating proxy container")
	resp, err := o.conn.Client().ContainerCreate(ctx, cfg, hostCfg, &dockernetwork.NetworkingConfig{}, nil, ProxyContainerName)
	if err != nil {
		return "", fmt.Errorf("portproxy: create proxy container: %w", err)
	}

	o.log.Debug("portproxy ops: starting proxy container id=%s", resp.ID[:12])
	if err := o.conn.Client().ContainerStart(ctx, resp.ID, dockercontainer.StartOptions{}); err != nil {
		return "", fmt.Errorf("portproxy: start proxy container: %w", err)
	}

	o.log.Info("portproxy ops: proxy container started: id=%s", resp.ID[:12])
	return resp.ID, nil
}

// CreateProxyNetwork creates a bridge network named "proxy_<containerName>"
// labelled with ProxyLabel.  If the network already exists its ID is returned
// without error (idempotent).
func (o *Ops) CreateProxyNetwork(ctx context.Context, containerName string) (string, error) {
	networkName := "proxy_" + containerName
	o.log.LowTrace("portproxy ops: creating network %s", networkName)

	resp, err := o.conn.Client().NetworkCreate(ctx, networkName, dockertypes.NetworkCreate{
		Driver: "bridge",
		Labels: map[string]string{ProxyLabel: ProxyLabelValue},
	})
	if err != nil {
		if !strings.Contains(err.Error(), "already exists") {
			return "", fmt.Errorf("portproxy: create network %q: %w", networkName, err)
		}
		// Network already exists — look up its ID.
		o.log.Debug("portproxy ops: network %s already exists, looking up ID", networkName)
		networks, listErr := o.conn.Client().NetworkList(ctx, dockertypes.NetworkListOptions{
			Filters: dockerfilters.NewArgs(dockerfilters.Arg("name", networkName)),
		})
		if listErr != nil {
			return "", fmt.Errorf("portproxy: list networks to find %q: %w", networkName, listErr)
		}
		for _, n := range networks {
			if n.Name == networkName {
				o.log.Info("portproxy ops: reusing existing network %s id=%s", networkName, n.ID[:12])
				return n.ID, nil
			}
		}
		return "", fmt.Errorf("portproxy: network %q reported as existing but not found in list", networkName)
	}

	o.log.Info("portproxy ops: created network %s id=%s", networkName, resp.ID[:12])
	return resp.ID, nil
}

// ConnectToProxyNetwork connects both the proxy container and the managed
// container to networkID.  The managed container is given an alias equal to
// its name so nginx can resolve it by name.  "Already connected" errors are
// silently ignored so the operation is idempotent.
//
// Connecting the proxy is retried up to 3 times with a 500 ms delay on
// "sandbox not found" errors, which Docker returns when the proxy container's
// network namespace is still being initialized (e.g. during a restart).
func (o *Ops) ConnectToProxyNetwork(ctx context.Context, networkID, proxyID, managedID, managedName string) error {
	o.log.LowTrace("portproxy ops: connecting containers to network %s", networkID)

	const maxAttempts = 10
	var proxyErr error
	for attempt := range maxAttempts {
		if attempt > 0 {
			time.Sleep(time.Second)
		}
		proxyErr = o.conn.Client().NetworkConnect(ctx, networkID, proxyID, nil)
		if proxyErr == nil {
			break
		}
		if strings.Contains(proxyErr.Error(), "already exists") {
			o.log.Debug("portproxy ops: proxy already connected to network %s", networkID)
			proxyErr = nil
			break
		}
		if strings.Contains(proxyErr.Error(), "sandbox") {
			o.log.Warn("portproxy ops: proxy sandbox not ready (attempt %d/%d), retrying...", attempt+1, maxAttempts)
			continue
		}
		break // non-retryable error
	}
	if proxyErr != nil {
		return fmt.Errorf("portproxy: connect proxy to network %q: %w", networkID, proxyErr)
	}

	if err := o.conn.Client().NetworkConnect(ctx, networkID, managedID, &dockernetwork.EndpointSettings{
		Aliases: []string{managedName},
	}); err != nil {
		if !strings.Contains(err.Error(), "already exists") {
			return fmt.Errorf("portproxy: connect managed container %q to network %q: %w", managedName, networkID, err)
		}
		o.log.Debug("portproxy ops: %s already connected to network %s", managedName, networkID)
	}

	o.log.Info("portproxy ops: connected proxy and %s to network %s", managedName, networkID)
	return nil
}

// DisconnectFromProxyNetwork disconnects both containers from the named
// network and then removes the network.  All errors are logged but the
// function does not return an error — cleanup is best-effort.
func (o *Ops) DisconnectFromProxyNetwork(ctx context.Context, networkName, proxyID, managedID string) error {
	o.log.LowTrace("portproxy ops: disconnecting from network %s (best-effort)", networkName)

	if err := o.conn.Client().NetworkDisconnect(ctx, networkName, proxyID, true); err != nil {
		o.log.Warn("portproxy ops: disconnect proxy from %s: %v", networkName, err)
	}
	if err := o.conn.Client().NetworkDisconnect(ctx, networkName, managedID, true); err != nil {
		o.log.Warn("portproxy ops: disconnect managed container from %s: %v", networkName, err)
	}
	if err := o.conn.Client().NetworkRemove(ctx, networkName); err != nil {
		o.log.Warn("portproxy ops: remove network %s: %v", networkName, err)
	}

	o.log.Info("portproxy ops: disconnected and removed network %s", networkName)
	return nil
}

// ReloadNginxConfig writes newConfig into the proxy container via exec and
// sends nginx a reload signal.  The write + reload is done in a single shell
// command using base64 to avoid quoting issues.  The function blocks until
// the exec finishes so callers know nginx has actually reloaded.
func (o *Ops) ReloadNginxConfig(ctx context.Context, proxyID, newConfig string) error {
	o.log.LowTrace("portproxy ops: reloading nginx config (proxyID=%s)", proxyID[:12])

	b64 := base64.StdEncoding.EncodeToString([]byte(newConfig))
	// Write to a temp file and atomically rename so a partial write from a
	// concurrent exec never leaves proxy.conf in a corrupt state.
	cmd := fmt.Sprintf(
		"echo %s | base64 -d > /etc/nginx/conf.d/proxy.conf.tmp && "+
			"mv /etc/nginx/conf.d/proxy.conf.tmp /etc/nginx/conf.d/proxy.conf && "+
			"nginx -s reload",
		b64,
	)

	execResp, err := o.conn.Client().ContainerExecCreate(ctx, proxyID, dockertypes.ExecConfig{
		Cmd:          []string{"sh", "-c", cmd},
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("portproxy: exec create for nginx reload: %w", err)
	}

	// Attach (not detach) so we can drain output and wait for completion.
	hijack, err := o.conn.Client().ContainerExecAttach(ctx, execResp.ID, dockertypes.ExecStartCheck{})
	if err != nil {
		return fmt.Errorf("portproxy: exec attach for nginx reload: %w", err)
	}
	defer hijack.Close()
	_, _ = io.Copy(io.Discard, hijack.Reader) // drain until EOF = command done

	info, err := o.conn.Client().ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return fmt.Errorf("portproxy: exec inspect for nginx reload: %w", err)
	}
	if info.ExitCode != 0 {
		return fmt.Errorf("portproxy: nginx reload exited with code %d", info.ExitCode)
	}

	o.log.Info("portproxy ops: nginx config reloaded (execID=%s)", execResp.ID[:12])
	return nil
}

// InspectContainer returns the full ContainerJSON for the given ref (name or ID).
func (o *Ops) InspectContainer(ctx context.Context, ref string) (dockertypes.ContainerJSON, error) {
	o.log.LowTrace("portproxy ops: inspecting container %s", ref)
	info, err := o.conn.Client().ContainerInspect(ctx, ref)
	if err != nil {
		return dockertypes.ContainerJSON{}, fmt.Errorf("portproxy: inspect container %q: %w", ref, err)
	}
	return info, nil
}

// ensureImage pulls ref if it is not already present locally.
func (o *Ops) ensureImage(ctx context.Context, ref string) error {
	_, _, err := o.conn.Client().ImageInspectWithRaw(ctx, ref)
	if err == nil {
		o.log.Debug("portproxy ops: image %s already present locally", ref)
		return nil
	}
	o.log.Info("portproxy ops: pulling image %s", ref)
	rc, err := o.conn.Client().ImagePull(ctx, ref, dockerimage.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull %q: %w", ref, err)
	}
	defer rc.Close()
	_, _ = io.Copy(io.Discard, rc) // drain the stream so the pull completes
	o.log.Info("portproxy ops: image %s pulled", ref)
	return nil
}
