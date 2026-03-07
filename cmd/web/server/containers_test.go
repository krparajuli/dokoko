package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	dockercontaineractor "dokoko.ai/dokoko/internal/docker/containers/actor"
	dockertypes "github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"
	dockernetwork "github.com/docker/docker/api/types/network"
)

// ── List containers ───────────────────────────────────────────────────────────

func TestListContainers_ReturnsContainers(t *testing.T) {
	fake := defaultFake()
	fake.containers = &fakeContainerActor{
		listFn: func(_ context.Context, _ dockercontainer.ListOptions) <-chan dockercontaineractor.ListResult {
			ch := make(chan dockercontaineractor.ListResult, 1)
			ch <- dockercontaineractor.ListResult{
				Containers: []dockertypes.Container{
					{ID: "abc123", Names: []string{"/web"}, Image: "nginx", State: "running"},
					{ID: "def456", Names: []string{"/db"}, Image: "postgres", State: "exited"},
				},
			}
			return ch
		},
	}

	rec := httptest.NewRecorder()
	newTestHandler(fake).listContainers(rec, newReq("GET", "/api/containers", nil))

	assertStatus(t, rec, http.StatusOK)
	resp := parseResp(t, rec)
	if !containsStr(string(resp.Data), "abc123") {
		t.Errorf("expected abc123 in response: %s", resp.Data)
	}
	if !containsStr(string(resp.Data), "def456") {
		t.Errorf("expected def456 in response: %s", resp.Data)
	}
}

func TestListContainers_Error(t *testing.T) {
	fake := defaultFake()
	fake.containers = &fakeContainerActor{
		listFn: func(_ context.Context, _ dockercontainer.ListOptions) <-chan dockercontaineractor.ListResult {
			ch := make(chan dockercontaineractor.ListResult, 1)
			ch <- dockercontaineractor.ListResult{Err: errors.New("daemon unreachable")}
			return ch
		},
	}

	rec := httptest.NewRecorder()
	newTestHandler(fake).listContainers(rec, newReq("GET", "/api/containers", nil))
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ── Create container ──────────────────────────────────────────────────────────

func TestCreateContainer_OK(t *testing.T) {
	var gotImage, gotName string
	fake := defaultFake()
	fake.containers = &fakeContainerActor{
		createFn: func(_ context.Context, name string, cfg *dockercontainer.Config, _ *dockercontainer.HostConfig, _ *dockernetwork.NetworkingConfig) (*dockercontaineractor.Ticket, error) {
			gotImage = cfg.Image
			gotName = name
			return closedContainerTicket("chg-create"), nil
		},
	}

	rec := httptest.NewRecorder()
	newTestHandler(fake).createContainer(rec, newReq("POST", "/api/containers", jsonBody(map[string]string{
		"image": "nginx:latest",
		"name":  "my-nginx",
	})))

	assertStatus(t, rec, http.StatusAccepted)
	if gotImage != "nginx:latest" {
		t.Errorf("image: got %q, want nginx:latest", gotImage)
	}
	if gotName != "my-nginx" {
		t.Errorf("name: got %q, want my-nginx", gotName)
	}
}

func TestCreateContainer_MissingImage(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestHandler(defaultFake()).createContainer(rec, newReq("POST", "/api/containers", jsonBody(map[string]string{"name": "x"})))
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestCreateContainer_DockerError(t *testing.T) {
	fake := defaultFake()
	fake.containers = &fakeContainerActor{
		createFn: func(_ context.Context, _ string, _ *dockercontainer.Config, _ *dockercontainer.HostConfig, _ *dockernetwork.NetworkingConfig) (*dockercontaineractor.Ticket, error) {
			return nil, errors.New("image not found")
		},
	}

	rec := httptest.NewRecorder()
	newTestHandler(fake).createContainer(rec, newReq("POST", "/api/containers", jsonBody(map[string]string{"image": "nonexistent:latest"})))
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ── Start container ───────────────────────────────────────────────────────────

func TestStartContainer_OK(t *testing.T) {
	var gotID string
	fake := defaultFake()
	fake.containers = &fakeContainerActor{
		startFn: func(_ context.Context, id string, _ dockercontainer.StartOptions) (*dockercontaineractor.Ticket, error) {
			gotID = id
			return closedContainerTicket("chg-start"), nil
		},
	}

	r := newReq("POST", "/api/containers/abc123/start", nil)
	r.SetPathValue("id", "abc123")
	rec := httptest.NewRecorder()
	newTestHandler(fake).startContainer(rec, r)

	assertStatus(t, rec, http.StatusAccepted)
	if gotID != "abc123" {
		t.Errorf("Start called with %q, want abc123", gotID)
	}
}

func TestStartContainer_Error(t *testing.T) {
	fake := defaultFake()
	fake.containers = &fakeContainerActor{
		startFn: func(_ context.Context, _ string, _ dockercontainer.StartOptions) (*dockercontaineractor.Ticket, error) {
			return nil, errors.New("container not found")
		},
	}

	r := newReq("POST", "/api/containers/bad/start", nil)
	r.SetPathValue("id", "bad")
	rec := httptest.NewRecorder()
	newTestHandler(fake).startContainer(rec, r)
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ── Stop container ────────────────────────────────────────────────────────────

func TestStopContainer_OK(t *testing.T) {
	var gotID string
	fake := defaultFake()
	fake.containers = &fakeContainerActor{
		stopFn: func(_ context.Context, id string, _ dockercontainer.StopOptions) (*dockercontaineractor.Ticket, error) {
			gotID = id
			return closedContainerTicket("chg-stop"), nil
		},
	}

	r := newReq("POST", "/api/containers/abc123/stop", nil)
	r.SetPathValue("id", "abc123")
	rec := httptest.NewRecorder()
	newTestHandler(fake).stopContainer(rec, r)

	assertStatus(t, rec, http.StatusAccepted)
	if gotID != "abc123" {
		t.Errorf("Stop called with %q, want abc123", gotID)
	}
}

// ── Remove container ──────────────────────────────────────────────────────────

func TestRemoveContainer_OK(t *testing.T) {
	var gotID string
	var gotForce bool
	fake := defaultFake()
	fake.containers = &fakeContainerActor{
		removeFn: func(_ context.Context, id string, opts dockercontainer.RemoveOptions) (*dockercontaineractor.Ticket, error) {
			gotID = id
			gotForce = opts.Force
			return closedContainerTicket("chg-rm"), nil
		},
	}

	r := newReq("DELETE", "/api/containers/abc123", nil)
	r.SetPathValue("id", "abc123")
	rec := httptest.NewRecorder()
	newTestHandler(fake).removeContainer(rec, r)

	assertStatus(t, rec, http.StatusAccepted)
	if gotID != "abc123" {
		t.Errorf("Remove called with %q, want abc123", gotID)
	}
	if !gotForce {
		t.Error("expected Force=true on remove")
	}
}

// ── Inspect container ─────────────────────────────────────────────────────────

func TestInspectContainer_OK(t *testing.T) {
	fake := defaultFake()
	fake.containers = &fakeContainerActor{
		inspectFn: func(_ context.Context, id string) <-chan dockercontaineractor.InspectResult {
			ch := make(chan dockercontaineractor.InspectResult, 1)
			ch <- dockercontaineractor.InspectResult{
				Info: dockertypes.ContainerJSON{
					ContainerJSONBase: &dockertypes.ContainerJSONBase{ID: id, Name: "/web"},
				},
			}
			return ch
		},
	}

	r := newReq("GET", "/api/containers/abc123/inspect", nil)
	r.SetPathValue("id", "abc123")
	rec := httptest.NewRecorder()
	newTestHandler(fake).inspectContainer(rec, r)

	assertStatus(t, rec, http.StatusOK)
	resp := parseResp(t, rec)
	if !containsStr(string(resp.Data), "abc123") {
		t.Errorf("inspect response missing container ID: %s", resp.Data)
	}
}

func TestInspectContainer_Error(t *testing.T) {
	fake := defaultFake()
	fake.containers = &fakeContainerActor{
		inspectFn: func(_ context.Context, _ string) <-chan dockercontaineractor.InspectResult {
			ch := make(chan dockercontaineractor.InspectResult, 1)
			ch <- dockercontaineractor.InspectResult{Err: errors.New("not found")}
			return ch
		},
	}

	r := newReq("GET", "/api/containers/bad/inspect", nil)
	r.SetPathValue("id", "bad")
	rec := httptest.NewRecorder()
	newTestHandler(fake).inspectContainer(rec, r)
	assertStatus(t, rec, http.StatusInternalServerError)
}
