// Package dockermanager owns the Docker connection lifecycle and all six
// resource subsystems (images, containers, builds, containerexec, networks,
// volumes).
//
// Resources with persistent inventories (images, networks, volumes, builds)
// are exposed through their Clerk, which wires the actor, state machine, and
// store together and calls Refresh on every connect to bootstrap the store
// with live Docker data.  Containers and exec have no persistent inventory and
// are exposed as raw actors.
//
// All stores and states survive reconnects — no operational history is lost.
//
// Typical usage:
//
//	mgr, err := dockermanager.New(ctx, log)
//	if err != nil { ... }
//	defer mgr.Close()
//
//	// Clerk-backed subsystems (store populated from Docker on connect):
//	ticket, err := mgr.Images().Pull(ctx, "ubuntu:22.04", opts)
//	records      := mgr.Images().Store().All()
//
//	// Raw-actor subsystems:
//	ticket, err := mgr.Containers().Create(ctx, ...)
//
// To recover from a lost connection:
//
//	if err := mgr.Ping(ctx); err != nil {
//	    if err := mgr.Reconnect(ctx); err != nil { ... }
//	}
package dockermanager

import (
	"context"
	"fmt"
	"sync"

	"dokoko.ai/dokoko/internal/docker"
	dockerbuildactor "dokoko.ai/dokoko/internal/docker/builds/actor"
	dockerbuildclerk "dokoko.ai/dokoko/internal/docker/builds/clerk"
	dockerbuildops "dokoko.ai/dokoko/internal/docker/builds/ops"
	dockerbuildstate "dokoko.ai/dokoko/internal/docker/builds/state"
	dockerexecactor "dokoko.ai/dokoko/internal/docker/containerexec/actor"
	dockerexecops "dokoko.ai/dokoko/internal/docker/containerexec/ops"
	dockerexecstate "dokoko.ai/dokoko/internal/docker/containerexec/state"
	dockercontaineractor "dokoko.ai/dokoko/internal/docker/containers/actor"
	dockercontainerops "dokoko.ai/dokoko/internal/docker/containers/ops"
	dockercontainerstate "dokoko.ai/dokoko/internal/docker/containers/state"
	dockerimageclerk "dokoko.ai/dokoko/internal/docker/images/clerk"
	dockerimageactor "dokoko.ai/dokoko/internal/docker/images/actor"
	dockerimageops "dokoko.ai/dokoko/internal/docker/images/ops"
	dockerimagestate "dokoko.ai/dokoko/internal/docker/images/state"
	dockernetworkclerk "dokoko.ai/dokoko/internal/docker/networks/clerk"
	dockernetworkactor "dokoko.ai/dokoko/internal/docker/networks/actor"
	dockernetworkops "dokoko.ai/dokoko/internal/docker/networks/ops"
	dockernetworkstate "dokoko.ai/dokoko/internal/docker/networks/state"
	dockervolumeclerk "dokoko.ai/dokoko/internal/docker/volumes/clerk"
	dockervolumeactor "dokoko.ai/dokoko/internal/docker/volumes/actor"
	dockervolumeops "dokoko.ai/dokoko/internal/docker/volumes/ops"
	dockervolumestate "dokoko.ai/dokoko/internal/docker/volumes/state"
	"dokoko.ai/dokoko/pkg/logger"
	dockerclient "github.com/docker/docker/client"
)

// Manager owns the Docker connection and all six resource subsystems.
// All exported methods are safe for concurrent use.
type Manager struct {
	mu   sync.RWMutex
	log  *logger.Logger
	opts []dockerclient.Opt // dialling options, reused verbatim on Reconnect

	conn *docker.Connection

	// States and stores are allocated once at construction and survive
	// reconnects so no operational history is ever lost.
	imageState     *dockerimagestate.State
	imageStore     *dockerimagestate.Store
	containerState *dockercontainerstate.State
	buildState     *dockerbuildstate.State
	buildStore     *dockerbuildstate.BuildStore
	execState      *dockerexecstate.State
	networkState   *dockernetworkstate.State
	networkStore   *dockernetworkstate.NetworkStore
	volumeState    *dockervolumestate.State
	volumeStore    *dockervolumestate.VolumeStore

	// Clerk-backed subsystems: Clerk = actor + state + store.
	// Recreated on every connect; stores (above) are passed in each time.
	images   *dockerimageclerk.Clerk
	builds   *dockerbuildclerk.Clerk
	networks *dockernetworkclerk.Clerk
	volumes  *dockervolumeclerk.Clerk

	// Underlying actors for clerk-backed subsystems, kept so shutdown() can
	// call Close() on them (clerks don't expose nor own the actor's Close).
	imageActor   *dockerimageactor.Actor
	buildActor   *dockerbuildactor.Actor
	networkActor *dockernetworkactor.Actor
	volumeActor  *dockervolumeactor.Actor

	// Raw-actor subsystems: no persistent store.
	// Recreated on every connect.
	containers *dockercontaineractor.Actor
	exec       *dockerexecactor.Actor
}

// New creates a Manager, establishes the initial Docker connection, starts all
// six resource subsystems, and bootstraps each store with live Docker data.
// Pass dockerclient.Opt values to override defaults (socket path, TLS, API
// version, etc.); they are reused verbatim by Reconnect.
func New(ctx context.Context, log *logger.Logger, opts ...dockerclient.Opt) (*Manager, error) {
	log.LowTrace("creating docker manager")

	m := &Manager{
		log:  log,
		opts: opts,

		imageState:     dockerimagestate.New(log),
		imageStore:     dockerimagestate.NewStore(log),
		containerState: dockercontainerstate.New(log),
		buildState:     dockerbuildstate.New(log),
		buildStore:     dockerbuildstate.NewBuildStore(log),
		execState:      dockerexecstate.New(log),
		networkState:   dockernetworkstate.New(log),
		networkStore:   dockernetworkstate.NewNetworkStore(log),
		volumeState:    dockervolumestate.New(log),
		volumeStore:    dockervolumestate.NewVolumeStore(log),
	}

	if err := m.connect(ctx); err != nil {
		return nil, fmt.Errorf("docker manager: %w", err)
	}

	log.Info("docker manager ready")
	return m, nil
}

// ── Connection management ─────────────────────────────────────────────────────

// Ping sends a ping to the Docker daemon to verify connectivity.
func (m *Manager) Ping(ctx context.Context) error {
	m.mu.RLock()
	conn := m.conn
	m.mu.RUnlock()

	if conn == nil {
		return fmt.Errorf("manager: not connected")
	}
	return conn.Ping(ctx)
}

// Reconnect gracefully shuts down all running subsystems, closes the current
// connection, re-dials Docker, restarts everything against the same state and
// store objects, and re-bootstraps each store from live Docker data.
//
// Safe to call concurrently; concurrent callers block until the first
// completes.
func (m *Manager) Reconnect(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Info("manager: reconnecting to docker daemon")

	m.shutdown()

	if m.conn != nil {
		if err := m.conn.Close(); err != nil {
			m.log.Warn("manager: error closing old connection during reconnect: %v", err)
		}
		m.conn = nil
	}

	if err := m.connect(ctx); err != nil {
		return fmt.Errorf("reconnect: %w", err)
	}

	m.log.Info("manager: reconnect complete")
	return nil
}

// Close gracefully shuts down all subsystems and the underlying connection.
// After Close the Manager must not be used.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.LowTrace("closing docker manager")
	m.shutdown()

	var err error
	if m.conn != nil {
		err = m.conn.Close()
		m.conn = nil
	}

	m.log.Info("docker manager closed")
	return err
}

// Refresh re-syncs all persistent stores with live Docker data.  Call this
// periodically or after a known mutation to keep stores current.
func (m *Manager) Refresh(ctx context.Context) error {
	m.mu.RLock()
	img, net, vol := m.images, m.networks, m.volumes
	m.mu.RUnlock()

	if img == nil {
		return fmt.Errorf("manager: not connected")
	}

	if err := img.Refresh(ctx); err != nil {
		return fmt.Errorf("refresh images: %w", err)
	}
	if err := net.Refresh(ctx); err != nil {
		return fmt.Errorf("refresh networks: %w", err)
	}
	if err := vol.Refresh(ctx); err != nil {
		return fmt.Errorf("refresh volumes: %w", err)
	}
	return nil
}

// ── Clerk / actor accessors ───────────────────────────────────────────────────

// Images returns the image Clerk (actor + state + store).
// Pull, Remove, Tag, List, Inspect, Exists, Refresh.
func (m *Manager) Images() *dockerimageclerk.Clerk {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.images
}

// Builds returns the build Clerk (actor + state + BuildStore).
// Build, PruneCache; the BuildStore event log is populated automatically.
func (m *Manager) Builds() *dockerbuildclerk.Clerk {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.builds
}

// Networks returns the network Clerk (actor + state + NetworkStore).
// Create, Remove, Prune, List, Inspect, Refresh.
func (m *Manager) Networks() *dockernetworkclerk.Clerk {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.networks
}

// Volumes returns the volume Clerk (actor + state + VolumeStore).
// Create, Remove, Prune, List, Inspect, Refresh.
func (m *Manager) Volumes() *dockervolumeclerk.Clerk {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.volumes
}

// Containers returns the container actor (no persistent store).
// Create, Start, Stop, Remove, Connect, Disconnect, List, Inspect, Exists.
func (m *Manager) Containers() *dockercontaineractor.Actor {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.containers
}

// Exec returns the container-exec actor (no persistent store).
// Create, Start, Attach, Inspect, Resize.
func (m *Manager) Exec() *dockerexecactor.Actor {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.exec
}

// ── State accessors ───────────────────────────────────────────────────────────

// ImageState returns the image operation state machine. Preserved across reconnects.
func (m *Manager) ImageState() *dockerimagestate.State { return m.imageState }

// ContainerState returns the container operation state machine. Preserved across reconnects.
func (m *Manager) ContainerState() *dockercontainerstate.State { return m.containerState }

// BuildState returns the build operation state machine. Preserved across reconnects.
func (m *Manager) BuildState() *dockerbuildstate.State { return m.buildState }

// ExecState returns the exec operation state machine. Preserved across reconnects.
func (m *Manager) ExecState() *dockerexecstate.State { return m.execState }

// NetworkState returns the network operation state machine. Preserved across reconnects.
func (m *Manager) NetworkState() *dockernetworkstate.State { return m.networkState }

// VolumeState returns the volume operation state machine. Preserved across reconnects.
func (m *Manager) VolumeState() *dockervolumestate.State { return m.volumeState }

// ── Internal helpers ──────────────────────────────────────────────────────────

// connect dials Docker, builds fresh ops, creates actors and clerks wired to
// the existing states and stores, then bootstraps the three live-inventory
// stores (images, networks, volumes) with a Refresh call.
// Must be called with m.mu held for writing, or before the Manager is shared.
func (m *Manager) connect(ctx context.Context) error {
	m.log.Debug("manager: dialling docker daemon")

	conn, err := docker.New(ctx, m.log, m.opts...)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	m.conn = conn

	// Build ops — each bound to the fresh connection.
	imageOps     := dockerimageops.New(conn, m.log)
	containerOps := dockercontainerops.New(conn, m.log)
	buildOps     := dockerbuildops.New(conn, m.log)
	execOps      := dockerexecops.New(conn, m.log)
	networkOps   := dockernetworkops.New(conn, m.log)
	volumeOps    := dockervolumeops.New(conn, m.log)

	// Create actors — each wired to the existing (preserved) state.
	// Clerk-backed actors are stored on the manager so shutdown() can Close them.
	m.imageActor   = dockerimageactor.New(imageOps, m.imageState, m.log, nil)
	m.buildActor   = dockerbuildactor.New(buildOps, m.buildState, m.log, nil)
	m.networkActor = dockernetworkactor.New(networkOps, m.networkState, m.log, nil)
	m.volumeActor  = dockervolumeactor.New(volumeOps, m.volumeState, m.log, nil)
	m.containers   = dockercontaineractor.New(containerOps, m.containerState, m.log, nil)
	m.exec         = dockerexecactor.New(execOps, m.execState, m.log, nil)

	// Wrap store-backed actors in their Clerk.
	m.images   = dockerimageclerk.New(m.imageActor, m.imageState, m.imageStore, m.log)
	m.builds   = dockerbuildclerk.New(m.buildActor, m.buildState, m.buildStore, m.log)
	m.networks = dockernetworkclerk.New(m.networkActor, m.networkState, m.networkStore, m.log)
	m.volumes  = dockervolumeclerk.New(m.volumeActor, m.volumeState, m.volumeStore, m.log)

	m.log.Debug("manager: all subsystems started, bootstrapping stores")

	// Populate the live-inventory stores from Docker.  A failure is logged as
	// a warning — the stores will be stale but the connection is still usable.
	if err := m.images.Refresh(ctx); err != nil {
		m.log.Warn("manager: image store refresh failed on connect: %v", err)
	}
	if err := m.networks.Refresh(ctx); err != nil {
		m.log.Warn("manager: network store refresh failed on connect: %v", err)
	}
	if err := m.volumes.Refresh(ctx); err != nil {
		m.log.Warn("manager: volume store refresh failed on connect: %v", err)
	}

	m.log.Debug("manager: store bootstrap complete")
	return nil
}

// shutdown closes every subsystem gracefully.
// Must be called with m.mu held for writing.
func (m *Manager) shutdown() {
	m.log.Debug("manager: shutting down all subsystems")

	// Builds clerk must be closed first to drain its watcher goroutines.
	if m.builds != nil {
		m.builds.Close()
		m.builds = nil
	}

	// Nil clerk references — stores are preserved and will be re-used on the
	// next connect(); actors (below) own the goroutine lifecycle.
	m.images = nil
	m.networks = nil
	m.volumes = nil

	// Close all actors. Clerks pass the actor to Docker operations but do not
	// call Close(); the manager is the sole owner of each actor's lifetime.
	if m.imageActor != nil {
		m.imageActor.Close()
		m.imageActor = nil
	}
	if m.buildActor != nil {
		m.buildActor.Close()
		m.buildActor = nil
	}
	if m.networkActor != nil {
		m.networkActor.Close()
		m.networkActor = nil
	}
	if m.volumeActor != nil {
		m.volumeActor.Close()
		m.volumeActor = nil
	}
	if m.containers != nil {
		m.containers.Close()
		m.containers = nil
	}
	if m.exec != nil {
		m.exec.Close()
		m.exec = nil
	}

	m.log.Debug("manager: all subsystems shut down")
}
