package server

// manager_iface.go defines narrow interfaces for each Docker subsystem used by
// the HTTP handlers, plus a Manager interface that wires them together.
//
// managerAdapter wraps *dockermanager.Manager so the concrete type satisfies
// Manager without the handlers importing the manager package directly.
// Tests inject a fakeManager instead.

import (
	"context"

	dockerexecactor "dokoko.ai/dokoko/internal/docker/containerexec/actor"
	dockercontaineractor "dokoko.ai/dokoko/internal/docker/containers/actor"
	dockerimageactor "dokoko.ai/dokoko/internal/docker/images/actor"
	dockerimagestate "dokoko.ai/dokoko/internal/docker/images/state"
	dockermanager "dokoko.ai/dokoko/internal/docker/manager"
	dockernetworkactor "dokoko.ai/dokoko/internal/docker/networks/actor"
	dockernetworkstate "dokoko.ai/dokoko/internal/docker/networks/state"
	dockervolumeactor "dokoko.ai/dokoko/internal/docker/volumes/actor"
	dockervolumestate "dokoko.ai/dokoko/internal/docker/volumes/state"
	dockertypes "github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"
	dockerfilters "github.com/docker/docker/api/types/filters"
	dockerimage "github.com/docker/docker/api/types/image"
	dockernetwork "github.com/docker/docker/api/types/network"
	dockervolume "github.com/docker/docker/api/types/volume"
)

// ── Subsystem interfaces ──────────────────────────────────────────────────────

// imageClerk is the subset of *dockerimageclerk.Clerk used by image handlers.
type imageClerk interface {
	Store() *dockerimagestate.Store
	Pull(ctx context.Context, ref string, opts dockerimage.PullOptions) (*dockerimageactor.Ticket, error)
	Remove(ctx context.Context, imageID string, opts dockerimage.RemoveOptions) (*dockerimageactor.Ticket, error)
	Tag(ctx context.Context, source, target string) (*dockerimageactor.Ticket, error)
	Refresh(ctx context.Context) error
	Inspect(ctx context.Context, imageID string) <-chan dockerimageactor.InspectResult
}

// containerActor is the subset of *dockercontaineractor.Actor used by container handlers.
type containerActor interface {
	List(ctx context.Context, opts dockercontainer.ListOptions) <-chan dockercontaineractor.ListResult
	Create(ctx context.Context, name string, config *dockercontainer.Config, hostConfig *dockercontainer.HostConfig, networkConfig *dockernetwork.NetworkingConfig) (*dockercontaineractor.Ticket, error)
	Start(ctx context.Context, containerID string, opts dockercontainer.StartOptions) (*dockercontaineractor.Ticket, error)
	Stop(ctx context.Context, containerID string, opts dockercontainer.StopOptions) (*dockercontaineractor.Ticket, error)
	Remove(ctx context.Context, containerID string, opts dockercontainer.RemoveOptions) (*dockercontaineractor.Ticket, error)
	Inspect(ctx context.Context, containerID string) <-chan dockercontaineractor.InspectResult
}

// volumeClerk is the subset of *dockervolumeclerk.Clerk used by volume handlers.
type volumeClerk interface {
	Store() *dockervolumestate.VolumeStore
	Create(ctx context.Context, opts dockervolume.CreateOptions) (*dockervolumeactor.Ticket, error)
	Remove(ctx context.Context, name string, force bool) (*dockervolumeactor.Ticket, error)
	Prune(ctx context.Context, pruneFilter dockerfilters.Args) (*dockervolumeactor.Ticket, error)
	Refresh(ctx context.Context) error
	Inspect(ctx context.Context, name string) <-chan dockervolumeactor.InspectResult
}

// networkClerk is the subset of *dockernetworkclerk.Clerk used by network handlers.
type networkClerk interface {
	Store() *dockernetworkstate.NetworkStore
	Create(ctx context.Context, name string, opts dockertypes.NetworkCreate) (*dockernetworkactor.Ticket, error)
	Remove(ctx context.Context, networkID string) (*dockernetworkactor.Ticket, error)
	Prune(ctx context.Context, pruneFilter dockerfilters.Args) (*dockernetworkactor.Ticket, error)
	Refresh(ctx context.Context) error
	Inspect(ctx context.Context, networkID string, opts dockertypes.NetworkInspectOptions) <-chan dockernetworkactor.InspectResult
}

// execActor is the subset of *dockerexecactor.Actor used by exec handlers.
type execActor interface {
	Create(ctx context.Context, containerID string, config dockertypes.ExecConfig) (*dockerexecactor.Ticket, error)
	ExecDockerID(changeID string) (string, error)
	Start(ctx context.Context, execID string, config dockertypes.ExecStartCheck) (*dockerexecactor.Ticket, error)
	Inspect(ctx context.Context, execID string) <-chan dockerexecactor.InspectResult
}

// stateSummarizer is implemented by all six state machines.
type stateSummarizer interface {
	Summary() (requested, active, failed, abandoned int)
}

// ── Top-level Manager interface ───────────────────────────────────────────────

// Manager is the complete interface the handler depends on.
// Satisfied by managerAdapter (wrapping *dockermanager.Manager) in production
// and by fakeManager in tests.
type Manager interface {
	Ping(ctx context.Context) error
	Images() imageClerk
	Containers() containerActor
	Volumes() volumeClerk
	Networks() networkClerk
	Exec() execActor
	ImageState() stateSummarizer
	ContainerState() stateSummarizer
	VolumeState() stateSummarizer
	NetworkState() stateSummarizer
	BuildState() stateSummarizer
	ExecState() stateSummarizer
}

// ── managerAdapter ────────────────────────────────────────────────────────────

// managerAdapter wraps *dockermanager.Manager and satisfies Manager.
// Concrete clerk/actor return types satisfy the narrow subsystem interfaces
// defined above via structural typing.
type managerAdapter struct{ m *dockermanager.Manager }

func newManagerAdapter(m *dockermanager.Manager) Manager { return &managerAdapter{m: m} }

func (a *managerAdapter) Ping(ctx context.Context) error    { return a.m.Ping(ctx) }
func (a *managerAdapter) Images() imageClerk                { return a.m.Images() }
func (a *managerAdapter) Containers() containerActor        { return a.m.Containers() }
func (a *managerAdapter) Volumes() volumeClerk              { return a.m.Volumes() }
func (a *managerAdapter) Networks() networkClerk            { return a.m.Networks() }
func (a *managerAdapter) Exec() execActor                   { return a.m.Exec() }
func (a *managerAdapter) ImageState() stateSummarizer       { return a.m.ImageState() }
func (a *managerAdapter) ContainerState() stateSummarizer   { return a.m.ContainerState() }
func (a *managerAdapter) VolumeState() stateSummarizer      { return a.m.VolumeState() }
func (a *managerAdapter) NetworkState() stateSummarizer     { return a.m.NetworkState() }
func (a *managerAdapter) BuildState() stateSummarizer       { return a.m.BuildState() }
func (a *managerAdapter) ExecState() stateSummarizer        { return a.m.ExecState() }
