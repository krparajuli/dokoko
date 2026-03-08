package server

// testutil_test.go — shared fakes, helpers, and response parsing for all
// handler tests.  Every test file in this package imports from here implicitly
// (same package, same build).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	dockerexecactor "dokoko.ai/dokoko/internal/docker/containerexec/actor"
	dockercontaineractor "dokoko.ai/dokoko/internal/docker/containers/actor"
	dockerimageactor "dokoko.ai/dokoko/internal/docker/images/actor"
	dockerimagestate "dokoko.ai/dokoko/internal/docker/images/state"
	dockernetworkactor "dokoko.ai/dokoko/internal/docker/networks/actor"
	dockernetworkstate "dokoko.ai/dokoko/internal/docker/networks/state"
	dockervolumeactor "dokoko.ai/dokoko/internal/docker/volumes/actor"
	dockervolumestate "dokoko.ai/dokoko/internal/docker/volumes/state"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"
	dockerfilters "github.com/docker/docker/api/types/filters"
	dockerimage "github.com/docker/docker/api/types/image"
	dockernetwork "github.com/docker/docker/api/types/network"
	dockervolume "github.com/docker/docker/api/types/volume"
)

// ── Logger ────────────────────────────────────────────────────────────────────

func silentLogger() *logger.Logger { return logger.New(logger.LevelSilent) }

// ── Closed ticket helpers ─────────────────────────────────────────────────────

func closedImageTicket(id string) *dockerimageactor.Ticket {
	done := make(chan struct{})
	close(done)
	return &dockerimageactor.Ticket{ChangeID: id, Done: done}
}

func closedContainerTicket(id string) *dockercontaineractor.Ticket {
	done := make(chan struct{})
	close(done)
	return &dockercontaineractor.Ticket{ChangeID: id, Done: done}
}

func closedVolumeTicket(id string) *dockervolumeactor.Ticket {
	done := make(chan struct{})
	close(done)
	return &dockervolumeactor.Ticket{ChangeID: id, Done: done}
}

func closedNetworkTicket(id string) *dockernetworkactor.Ticket {
	done := make(chan struct{})
	close(done)
	return &dockernetworkactor.Ticket{ChangeID: id, Done: done}
}

func closedExecTicket(id string) *dockerexecactor.Ticket {
	done := make(chan struct{})
	close(done)
	return &dockerexecactor.Ticket{ChangeID: id, Done: done}
}

// ── errTicket — returns an error from mutation calls ─────────────────────────

var errFake = errors.New("fake error")

// ── fakeImageClerk ────────────────────────────────────────────────────────────

type fakeImageClerk struct {
	store     *dockerimagestate.Store
	pullFn    func(ctx context.Context, ref string, opts dockerimage.PullOptions) (*dockerimageactor.Ticket, error)
	removeFn  func(ctx context.Context, imageID string, opts dockerimage.RemoveOptions) (*dockerimageactor.Ticket, error)
	tagFn     func(ctx context.Context, source, target string) (*dockerimageactor.Ticket, error)
	refreshFn func(ctx context.Context) error
	inspectFn func(ctx context.Context, imageID string) <-chan dockerimageactor.InspectResult
}

func (f *fakeImageClerk) Store() *dockerimagestate.Store {
	if f.store != nil {
		return f.store
	}
	return dockerimagestate.NewStore(silentLogger())
}

func (f *fakeImageClerk) Pull(ctx context.Context, ref string, opts dockerimage.PullOptions) (*dockerimageactor.Ticket, error) {
	if f.pullFn != nil {
		return f.pullFn(ctx, ref, opts)
	}
	return closedImageTicket("chg-pull"), nil
}

func (f *fakeImageClerk) Remove(ctx context.Context, imageID string, opts dockerimage.RemoveOptions) (*dockerimageactor.Ticket, error) {
	if f.removeFn != nil {
		return f.removeFn(ctx, imageID, opts)
	}
	return closedImageTicket("chg-remove"), nil
}

func (f *fakeImageClerk) Tag(ctx context.Context, source, target string) (*dockerimageactor.Ticket, error) {
	if f.tagFn != nil {
		return f.tagFn(ctx, source, target)
	}
	return closedImageTicket("chg-tag"), nil
}

func (f *fakeImageClerk) Refresh(ctx context.Context) error {
	if f.refreshFn != nil {
		return f.refreshFn(ctx)
	}
	return nil
}

func (f *fakeImageClerk) Inspect(ctx context.Context, imageID string) <-chan dockerimageactor.InspectResult {
	if f.inspectFn != nil {
		return f.inspectFn(ctx, imageID)
	}
	ch := make(chan dockerimageactor.InspectResult, 1)
	ch <- dockerimageactor.InspectResult{}
	return ch
}

// ── fakeContainerActor ────────────────────────────────────────────────────────

type fakeContainerActor struct {
	listFn    func(ctx context.Context, opts dockercontainer.ListOptions) <-chan dockercontaineractor.ListResult
	createFn  func(ctx context.Context, name string, cfg *dockercontainer.Config, hc *dockercontainer.HostConfig, nc *dockernetwork.NetworkingConfig) (*dockercontaineractor.Ticket, error)
	startFn   func(ctx context.Context, id string, opts dockercontainer.StartOptions) (*dockercontaineractor.Ticket, error)
	stopFn    func(ctx context.Context, id string, opts dockercontainer.StopOptions) (*dockercontaineractor.Ticket, error)
	removeFn  func(ctx context.Context, id string, opts dockercontainer.RemoveOptions) (*dockercontaineractor.Ticket, error)
	inspectFn func(ctx context.Context, id string) <-chan dockercontaineractor.InspectResult
}

func (f *fakeContainerActor) List(ctx context.Context, opts dockercontainer.ListOptions) <-chan dockercontaineractor.ListResult {
	if f.listFn != nil {
		return f.listFn(ctx, opts)
	}
	ch := make(chan dockercontaineractor.ListResult, 1)
	ch <- dockercontaineractor.ListResult{}
	return ch
}

func (f *fakeContainerActor) Create(ctx context.Context, name string, cfg *dockercontainer.Config, hc *dockercontainer.HostConfig, nc *dockernetwork.NetworkingConfig) (*dockercontaineractor.Ticket, error) {
	if f.createFn != nil {
		return f.createFn(ctx, name, cfg, hc, nc)
	}
	return closedContainerTicket("chg-create"), nil
}

func (f *fakeContainerActor) Start(ctx context.Context, id string, opts dockercontainer.StartOptions) (*dockercontaineractor.Ticket, error) {
	if f.startFn != nil {
		return f.startFn(ctx, id, opts)
	}
	return closedContainerTicket("chg-start"), nil
}

func (f *fakeContainerActor) Stop(ctx context.Context, id string, opts dockercontainer.StopOptions) (*dockercontaineractor.Ticket, error) {
	if f.stopFn != nil {
		return f.stopFn(ctx, id, opts)
	}
	return closedContainerTicket("chg-stop"), nil
}

func (f *fakeContainerActor) Remove(ctx context.Context, id string, opts dockercontainer.RemoveOptions) (*dockercontaineractor.Ticket, error) {
	if f.removeFn != nil {
		return f.removeFn(ctx, id, opts)
	}
	return closedContainerTicket("chg-remove"), nil
}

func (f *fakeContainerActor) Inspect(ctx context.Context, id string) <-chan dockercontaineractor.InspectResult {
	if f.inspectFn != nil {
		return f.inspectFn(ctx, id)
	}
	ch := make(chan dockercontaineractor.InspectResult, 1)
	ch <- dockercontaineractor.InspectResult{}
	return ch
}

// ── fakeVolumeClerk ───────────────────────────────────────────────────────────

type fakeVolumeClerk struct {
	store     *dockervolumestate.VolumeStore
	createFn  func(ctx context.Context, opts dockervolume.CreateOptions) (*dockervolumeactor.Ticket, error)
	removeFn  func(ctx context.Context, name string, force bool) (*dockervolumeactor.Ticket, error)
	pruneFn   func(ctx context.Context, f dockerfilters.Args) (*dockervolumeactor.Ticket, error)
	refreshFn func(ctx context.Context) error
	inspectFn func(ctx context.Context, name string) <-chan dockervolumeactor.InspectResult
}

func (f *fakeVolumeClerk) Store() *dockervolumestate.VolumeStore {
	if f.store != nil {
		return f.store
	}
	return dockervolumestate.NewVolumeStore(silentLogger())
}

func (f *fakeVolumeClerk) Create(ctx context.Context, opts dockervolume.CreateOptions) (*dockervolumeactor.Ticket, error) {
	if f.createFn != nil {
		return f.createFn(ctx, opts)
	}
	return closedVolumeTicket("chg-create"), nil
}

func (f *fakeVolumeClerk) Remove(ctx context.Context, name string, force bool) (*dockervolumeactor.Ticket, error) {
	if f.removeFn != nil {
		return f.removeFn(ctx, name, force)
	}
	return closedVolumeTicket("chg-remove"), nil
}

func (f *fakeVolumeClerk) Prune(ctx context.Context, fil dockerfilters.Args) (*dockervolumeactor.Ticket, error) {
	if f.pruneFn != nil {
		return f.pruneFn(ctx, fil)
	}
	return closedVolumeTicket("chg-prune"), nil
}

func (f *fakeVolumeClerk) Refresh(ctx context.Context) error {
	if f.refreshFn != nil {
		return f.refreshFn(ctx)
	}
	return nil
}

func (f *fakeVolumeClerk) Inspect(ctx context.Context, name string) <-chan dockervolumeactor.InspectResult {
	if f.inspectFn != nil {
		return f.inspectFn(ctx, name)
	}
	ch := make(chan dockervolumeactor.InspectResult, 1)
	ch <- dockervolumeactor.InspectResult{}
	return ch
}

// ── fakeNetworkClerk ──────────────────────────────────────────────────────────

type fakeNetworkClerk struct {
	store     *dockernetworkstate.NetworkStore
	createFn  func(ctx context.Context, name string, opts dockertypes.NetworkCreate) (*dockernetworkactor.Ticket, error)
	removeFn  func(ctx context.Context, networkID string) (*dockernetworkactor.Ticket, error)
	pruneFn   func(ctx context.Context, f dockerfilters.Args) (*dockernetworkactor.Ticket, error)
	refreshFn func(ctx context.Context) error
	inspectFn func(ctx context.Context, id string, opts dockertypes.NetworkInspectOptions) <-chan dockernetworkactor.InspectResult
}

func (f *fakeNetworkClerk) Store() *dockernetworkstate.NetworkStore {
	if f.store != nil {
		return f.store
	}
	return dockernetworkstate.NewNetworkStore(silentLogger())
}

func (f *fakeNetworkClerk) Create(ctx context.Context, name string, opts dockertypes.NetworkCreate) (*dockernetworkactor.Ticket, error) {
	if f.createFn != nil {
		return f.createFn(ctx, name, opts)
	}
	return closedNetworkTicket("chg-create"), nil
}

func (f *fakeNetworkClerk) Remove(ctx context.Context, networkID string) (*dockernetworkactor.Ticket, error) {
	if f.removeFn != nil {
		return f.removeFn(ctx, networkID)
	}
	return closedNetworkTicket("chg-remove"), nil
}

func (f *fakeNetworkClerk) Prune(ctx context.Context, fil dockerfilters.Args) (*dockernetworkactor.Ticket, error) {
	if f.pruneFn != nil {
		return f.pruneFn(ctx, fil)
	}
	return closedNetworkTicket("chg-prune"), nil
}

func (f *fakeNetworkClerk) Refresh(ctx context.Context) error {
	if f.refreshFn != nil {
		return f.refreshFn(ctx)
	}
	return nil
}

func (f *fakeNetworkClerk) Inspect(ctx context.Context, id string, opts dockertypes.NetworkInspectOptions) <-chan dockernetworkactor.InspectResult {
	if f.inspectFn != nil {
		return f.inspectFn(ctx, id, opts)
	}
	ch := make(chan dockernetworkactor.InspectResult, 1)
	ch <- dockernetworkactor.InspectResult{}
	return ch
}

// ── fakeExecActor ─────────────────────────────────────────────────────────────

type fakeExecActor struct {
	createFn       func(ctx context.Context, containerID string, cfg dockertypes.ExecConfig) (*dockerexecactor.Ticket, error)
	execDockerIDFn func(changeID string) (string, error)
	startFn        func(ctx context.Context, execID string, cfg dockertypes.ExecStartCheck) (*dockerexecactor.Ticket, error)
	inspectFn      func(ctx context.Context, execID string) <-chan dockerexecactor.InspectResult
}

func (f *fakeExecActor) Create(ctx context.Context, containerID string, cfg dockertypes.ExecConfig) (*dockerexecactor.Ticket, error) {
	if f.createFn != nil {
		return f.createFn(ctx, containerID, cfg)
	}
	return closedExecTicket("chg-create"), nil
}

func (f *fakeExecActor) ExecDockerID(changeID string) (string, error) {
	if f.execDockerIDFn != nil {
		return f.execDockerIDFn(changeID)
	}
	return "fake-exec-id-" + changeID, nil
}

func (f *fakeExecActor) Start(ctx context.Context, execID string, cfg dockertypes.ExecStartCheck) (*dockerexecactor.Ticket, error) {
	if f.startFn != nil {
		return f.startFn(ctx, execID, cfg)
	}
	return closedExecTicket("chg-start"), nil
}

func (f *fakeExecActor) Inspect(ctx context.Context, execID string) <-chan dockerexecactor.InspectResult {
	if f.inspectFn != nil {
		return f.inspectFn(ctx, execID)
	}
	ch := make(chan dockerexecactor.InspectResult, 1)
	ch <- dockerexecactor.InspectResult{}
	return ch
}

// ── fakeStateSummarizer ───────────────────────────────────────────────────────

type fakeStateSumm struct{ req, act, fail, aband int }

func (f *fakeStateSumm) Summary() (int, int, int, int) { return f.req, f.act, f.fail, f.aband }

// ── fakeManager ───────────────────────────────────────────────────────────────

type fakeManager struct {
	images     imageClerk
	containers containerActor
	volumes    volumeClerk
	networks   networkClerk
	exec       execActor
	pingErr    error
	summ       *fakeStateSumm
}

func defaultFake() *fakeManager {
	return &fakeManager{
		images:     &fakeImageClerk{},
		containers: &fakeContainerActor{},
		volumes:    &fakeVolumeClerk{},
		networks:   &fakeNetworkClerk{},
		exec:       &fakeExecActor{},
		summ:       &fakeStateSumm{},
	}
}

func (f *fakeManager) Ping(_ context.Context) error        { return f.pingErr }
func (f *fakeManager) Images() imageClerk                  { return f.images }
func (f *fakeManager) Containers() containerActor          { return f.containers }
func (f *fakeManager) Volumes() volumeClerk                { return f.volumes }
func (f *fakeManager) Networks() networkClerk              { return f.networks }
func (f *fakeManager) Exec() execActor                     { return f.exec }
func (f *fakeManager) PortProxy() portProxyClerk           { return nil }
func (f *fakeManager) WebContainers() webContainersClerk   { return nil }
func (f *fakeManager) ImageState() stateSummarizer         { return f.summ }
func (f *fakeManager) ContainerState() stateSummarizer     { return f.summ }
func (f *fakeManager) VolumeState() stateSummarizer        { return f.summ }
func (f *fakeManager) NetworkState() stateSummarizer       { return f.summ }
func (f *fakeManager) BuildState() stateSummarizer         { return f.summ }
func (f *fakeManager) ExecState() stateSummarizer          { return f.summ }

// ── HTTP test helpers ─────────────────────────────────────────────────────────

func newTestHandler(m Manager) *handler {
	return &handler{mgr: m, log: silentLogger()}
}

// apiResp is the outer envelope returned by all JSON handlers.
type apiResp struct {
	Data    json.RawMessage `json:"data"`
	Message string          `json:"message"`
	Error   string          `json:"error"`
}

func parseResp(t *testing.T, rec *httptest.ResponseRecorder) apiResp {
	t.Helper()
	var r apiResp
	if err := json.NewDecoder(rec.Body).Decode(&r); err != nil {
		t.Fatalf("parse response body: %v", err)
	}
	return r
}

func jsonBody(v any) io.Reader {
	b := &bytes.Buffer{}
	_ = json.NewEncoder(b).Encode(v)
	return b
}

func newReq(method, path string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, path, body)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	return r
}

func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Errorf("status: got %d, want %d (body: %s)", rec.Code, want, rec.Body.String())
	}
}
