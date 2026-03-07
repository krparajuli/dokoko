package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	dockernetworkactor "dokoko.ai/dokoko/internal/docker/networks/actor"
	dockernetworkstate "dokoko.ai/dokoko/internal/docker/networks/state"
	dockertypes "github.com/docker/docker/api/types"
	dockerfilters "github.com/docker/docker/api/types/filters"
)

// ── List networks ─────────────────────────────────────────────────────────────

func TestListNetworks_ReturnsStoreRecords(t *testing.T) {
	store := dockernetworkstate.NewNetworkStore(silentLogger())
	store.Register(dockernetworkstate.RegisterNetworkParams{
		DockerID: "abc123def456",
		Name:     "bridge",
		Driver:   "bridge",
		Origin:   dockernetworkstate.NetworkOriginOutOfBand,
	})

	fake := defaultFake()
	fake.networks = &fakeNetworkClerk{store: store}

	rec := httptest.NewRecorder()
	newTestHandler(fake).listNetworks(rec, newReq("GET", "/api/networks", nil))

	assertStatus(t, rec, http.StatusOK)
	resp := parseResp(t, rec)
	if !containsStr(string(resp.Data), "bridge") {
		t.Errorf("response missing network: %s", resp.Data)
	}
}

// ── Create network ────────────────────────────────────────────────────────────

func TestCreateNetwork_OK(t *testing.T) {
	var gotName string
	var gotDriver string
	fake := defaultFake()
	fake.networks = &fakeNetworkClerk{
		createFn: func(_ context.Context, name string, opts dockertypes.NetworkCreate) (*dockernetworkactor.Ticket, error) {
			gotName = name
			gotDriver = opts.Driver
			return closedNetworkTicket("chg-create"), nil
		},
	}

	rec := httptest.NewRecorder()
	newTestHandler(fake).createNetwork(rec, newReq("POST", "/api/networks", jsonBody(map[string]string{
		"name":   "my-net",
		"driver": "bridge",
	})))

	assertStatus(t, rec, http.StatusAccepted)
	if gotName != "my-net" {
		t.Errorf("name: got %q, want my-net", gotName)
	}
	if gotDriver != "bridge" {
		t.Errorf("driver: got %q, want bridge", gotDriver)
	}
}

func TestCreateNetwork_MissingName(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestHandler(defaultFake()).createNetwork(rec, newReq("POST", "/api/networks", jsonBody(map[string]string{"driver": "bridge"})))
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestCreateNetwork_DefaultsBridgeDriver(t *testing.T) {
	var gotDriver string
	fake := defaultFake()
	fake.networks = &fakeNetworkClerk{
		createFn: func(_ context.Context, _ string, opts dockertypes.NetworkCreate) (*dockernetworkactor.Ticket, error) {
			gotDriver = opts.Driver
			return closedNetworkTicket("chg-create"), nil
		},
	}

	rec := httptest.NewRecorder()
	newTestHandler(fake).createNetwork(rec, newReq("POST", "/api/networks", jsonBody(map[string]string{"name": "x"})))

	assertStatus(t, rec, http.StatusAccepted)
	if gotDriver != "bridge" {
		t.Errorf("driver defaulted to %q, want bridge", gotDriver)
	}
}

// ── Remove network ────────────────────────────────────────────────────────────

func TestRemoveNetwork_OK(t *testing.T) {
	var gotID string
	fake := defaultFake()
	fake.networks = &fakeNetworkClerk{
		removeFn: func(_ context.Context, id string) (*dockernetworkactor.Ticket, error) {
			gotID = id
			return closedNetworkTicket("chg-rm"), nil
		},
	}

	r := newReq("DELETE", "/api/networks/abc123/", nil)
	r.SetPathValue("id", "abc123")
	rec := httptest.NewRecorder()
	newTestHandler(fake).removeNetwork(rec, r)

	assertStatus(t, rec, http.StatusAccepted)
	if gotID != "abc123" {
		t.Errorf("Remove called with %q, want abc123", gotID)
	}
}

// ── Prune networks ────────────────────────────────────────────────────────────

func TestPruneNetworks_OK(t *testing.T) {
	called := false
	fake := defaultFake()
	fake.networks = &fakeNetworkClerk{
		pruneFn: func(_ context.Context, _ dockerfilters.Args) (*dockernetworkactor.Ticket, error) {
			called = true
			return closedNetworkTicket("chg-prune"), nil
		},
	}

	rec := httptest.NewRecorder()
	newTestHandler(fake).pruneNetworks(rec, newReq("POST", "/api/networks/prune", nil))

	assertStatus(t, rec, http.StatusAccepted)
	if !called {
		t.Error("Prune was not called")
	}
}

// ── Refresh networks ──────────────────────────────────────────────────────────

func TestRefreshNetworks_OK(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestHandler(defaultFake()).refreshNetworks(rec, newReq("POST", "/api/networks/refresh", nil))
	assertStatus(t, rec, http.StatusOK)
}

func TestRefreshNetworks_Error(t *testing.T) {
	fake := defaultFake()
	fake.networks = &fakeNetworkClerk{
		refreshFn: func(_ context.Context) error { return errors.New("daemon down") },
	}

	rec := httptest.NewRecorder()
	newTestHandler(fake).refreshNetworks(rec, newReq("POST", "/api/networks/refresh", nil))
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ── Inspect network ───────────────────────────────────────────────────────────

func TestInspectNetwork_OK(t *testing.T) {
	fake := defaultFake()
	fake.networks = &fakeNetworkClerk{
		inspectFn: func(_ context.Context, id string, _ dockertypes.NetworkInspectOptions) <-chan dockernetworkactor.InspectResult {
			ch := make(chan dockernetworkactor.InspectResult, 1)
			ch <- dockernetworkactor.InspectResult{
				Network: dockertypes.NetworkResource{ID: id, Name: "my-net", Driver: "bridge"},
			}
			return ch
		},
	}

	r := newReq("GET", "/api/networks/abc123/inspect", nil)
	r.SetPathValue("id", "abc123")
	rec := httptest.NewRecorder()
	newTestHandler(fake).inspectNetwork(rec, r)

	assertStatus(t, rec, http.StatusOK)
	resp := parseResp(t, rec)
	if !containsStr(string(resp.Data), "my-net") {
		t.Errorf("inspect response missing network name: %s", resp.Data)
	}
}

func TestInspectNetwork_Error(t *testing.T) {
	fake := defaultFake()
	fake.networks = &fakeNetworkClerk{
		inspectFn: func(_ context.Context, _ string, _ dockertypes.NetworkInspectOptions) <-chan dockernetworkactor.InspectResult {
			ch := make(chan dockernetworkactor.InspectResult, 1)
			ch <- dockernetworkactor.InspectResult{Err: errors.New("not found")}
			return ch
		},
	}

	r := newReq("GET", "/api/networks/bad/inspect", nil)
	r.SetPathValue("id", "bad")
	rec := httptest.NewRecorder()
	newTestHandler(fake).inspectNetwork(rec, r)
	assertStatus(t, rec, http.StatusInternalServerError)
}
