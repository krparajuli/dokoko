package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	dockervolumeactor "dokoko.ai/dokoko/internal/docker/volumes/actor"
	dockervolumestate "dokoko.ai/dokoko/internal/docker/volumes/state"
	dockerfilters "github.com/docker/docker/api/types/filters"
	dockervolume "github.com/docker/docker/api/types/volume"
)

// ── List volumes ──────────────────────────────────────────────────────────────

func TestListVolumes_ReturnsStoreRecords(t *testing.T) {
	store := dockervolumestate.NewVolumeStore(silentLogger())
	store.Register(dockervolumestate.RegisterVolumeParams{
		Name:   "my-vol",
		Driver: "local",
		Origin: dockervolumestate.VolumeOriginOutOfBand,
	})

	fake := defaultFake()
	fake.volumes = &fakeVolumeClerk{store: store}

	rec := httptest.NewRecorder()
	newTestHandler(fake).listVolumes(rec, newReq("GET", "/api/volumes", nil))

	assertStatus(t, rec, http.StatusOK)
	resp := parseResp(t, rec)
	if !containsStr(string(resp.Data), "my-vol") {
		t.Errorf("response missing my-vol: %s", resp.Data)
	}
}

func TestListVolumes_EmptyStore(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestHandler(defaultFake()).listVolumes(rec, newReq("GET", "/api/volumes", nil))
	assertStatus(t, rec, http.StatusOK)
}

// ── Create volume ─────────────────────────────────────────────────────────────

func TestCreateVolume_OK(t *testing.T) {
	var gotOpts dockervolume.CreateOptions
	fake := defaultFake()
	fake.volumes = &fakeVolumeClerk{
		createFn: func(_ context.Context, opts dockervolume.CreateOptions) (*dockervolumeactor.Ticket, error) {
			gotOpts = opts
			return closedVolumeTicket("chg-create"), nil
		},
	}

	rec := httptest.NewRecorder()
	newTestHandler(fake).createVolume(rec, newReq("POST", "/api/volumes", jsonBody(map[string]string{
		"name":   "data-vol",
		"driver": "local",
	})))

	assertStatus(t, rec, http.StatusAccepted)
	if gotOpts.Name != "data-vol" {
		t.Errorf("name: got %q, want data-vol", gotOpts.Name)
	}
	if gotOpts.Driver != "local" {
		t.Errorf("driver: got %q, want local", gotOpts.Driver)
	}
}

func TestCreateVolume_DefaultsToLocalDriver(t *testing.T) {
	var gotOpts dockervolume.CreateOptions
	fake := defaultFake()
	fake.volumes = &fakeVolumeClerk{
		createFn: func(_ context.Context, opts dockervolume.CreateOptions) (*dockervolumeactor.Ticket, error) {
			gotOpts = opts
			return closedVolumeTicket("chg-create"), nil
		},
	}

	rec := httptest.NewRecorder()
	newTestHandler(fake).createVolume(rec, newReq("POST", "/api/volumes", jsonBody(map[string]string{"name": "x"})))

	assertStatus(t, rec, http.StatusAccepted)
	if gotOpts.Driver != "local" {
		t.Errorf("driver defaulted to %q, want local", gotOpts.Driver)
	}
}

func TestCreateVolume_Error(t *testing.T) {
	fake := defaultFake()
	fake.volumes = &fakeVolumeClerk{
		createFn: func(_ context.Context, _ dockervolume.CreateOptions) (*dockervolumeactor.Ticket, error) {
			return nil, errors.New("driver error")
		},
	}

	rec := httptest.NewRecorder()
	newTestHandler(fake).createVolume(rec, newReq("POST", "/api/volumes", jsonBody(map[string]string{"name": "x"})))
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ── Remove volume ─────────────────────────────────────────────────────────────

func TestRemoveVolume_OK(t *testing.T) {
	var gotName string
	fake := defaultFake()
	fake.volumes = &fakeVolumeClerk{
		removeFn: func(_ context.Context, name string, _ bool) (*dockervolumeactor.Ticket, error) {
			gotName = name
			return closedVolumeTicket("chg-rm"), nil
		},
	}

	r := newReq("DELETE", "/api/volumes/my-vol", nil)
	r.SetPathValue("name", "my-vol")
	rec := httptest.NewRecorder()
	newTestHandler(fake).removeVolume(rec, r)

	assertStatus(t, rec, http.StatusAccepted)
	if gotName != "my-vol" {
		t.Errorf("Remove called with %q, want my-vol", gotName)
	}
}

// ── Prune volumes ─────────────────────────────────────────────────────────────

func TestPruneVolumes_OK(t *testing.T) {
	called := false
	fake := defaultFake()
	fake.volumes = &fakeVolumeClerk{
		pruneFn: func(_ context.Context, _ dockerfilters.Args) (*dockervolumeactor.Ticket, error) {
			called = true
			return closedVolumeTicket("chg-prune"), nil
		},
	}

	rec := httptest.NewRecorder()
	newTestHandler(fake).pruneVolumes(rec, newReq("POST", "/api/volumes/prune", nil))

	assertStatus(t, rec, http.StatusAccepted)
	if !called {
		t.Error("Prune was not called")
	}
}

// ── Refresh volumes ───────────────────────────────────────────────────────────

func TestRefreshVolumes_OK(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestHandler(defaultFake()).refreshVolumes(rec, newReq("POST", "/api/volumes/refresh", nil))
	assertStatus(t, rec, http.StatusOK)
}

func TestRefreshVolumes_Error(t *testing.T) {
	fake := defaultFake()
	fake.volumes = &fakeVolumeClerk{
		refreshFn: func(_ context.Context) error { return errors.New("daemon down") },
	}

	rec := httptest.NewRecorder()
	newTestHandler(fake).refreshVolumes(rec, newReq("POST", "/api/volumes/refresh", nil))
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ── Inspect volume ────────────────────────────────────────────────────────────

func TestInspectVolume_OK(t *testing.T) {
	fake := defaultFake()
	fake.volumes = &fakeVolumeClerk{
		inspectFn: func(_ context.Context, name string) <-chan dockervolumeactor.InspectResult {
			ch := make(chan dockervolumeactor.InspectResult, 1)
			ch <- dockervolumeactor.InspectResult{
				Volume: dockervolume.Volume{Name: name, Driver: "local"},
			}
			return ch
		},
	}

	r := newReq("GET", "/api/volumes/my-vol/inspect", nil)
	r.SetPathValue("name", "my-vol")
	rec := httptest.NewRecorder()
	newTestHandler(fake).inspectVolume(rec, r)

	assertStatus(t, rec, http.StatusOK)
	resp := parseResp(t, rec)
	if !containsStr(string(resp.Data), "my-vol") {
		t.Errorf("inspect response missing name: %s", resp.Data)
	}
}

func TestInspectVolume_Error(t *testing.T) {
	fake := defaultFake()
	fake.volumes = &fakeVolumeClerk{
		inspectFn: func(_ context.Context, _ string) <-chan dockervolumeactor.InspectResult {
			ch := make(chan dockervolumeactor.InspectResult, 1)
			ch <- dockervolumeactor.InspectResult{Err: errors.New("not found")}
			return ch
		},
	}

	r := newReq("GET", "/api/volumes/missing/inspect", nil)
	r.SetPathValue("name", "missing")
	rec := httptest.NewRecorder()
	newTestHandler(fake).inspectVolume(rec, r)
	assertStatus(t, rec, http.StatusInternalServerError)
}
