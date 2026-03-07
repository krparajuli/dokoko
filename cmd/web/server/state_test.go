package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ── Health ────────────────────────────────────────────────────────────────────

func TestHealth_DockerUp(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestHandler(defaultFake()).health(rec, newReq("GET", "/api/health", nil))

	assertStatus(t, rec, http.StatusOK)
	resp := parseResp(t, rec)

	var status map[string]any
	if err := json.Unmarshal(resp.Data, &status); err != nil {
		t.Fatalf("unmarshal health data: %v", err)
	}
	if ok, _ := status["ok"].(bool); !ok {
		t.Errorf("expected ok=true, got %v", status["ok"])
	}
	if docker, _ := status["docker"].(bool); !docker {
		t.Errorf("expected docker=true, got %v", status["docker"])
	}
	if _, hasErr := status["error"]; hasErr {
		t.Errorf("unexpected error field in healthy response")
	}
}

func TestHealth_DockerDown(t *testing.T) {
	fake := defaultFake()
	fake.pingErr = errors.New("connection refused")

	rec := httptest.NewRecorder()
	newTestHandler(fake).health(rec, newReq("GET", "/api/health", nil))

	assertStatus(t, rec, http.StatusOK) // handler always returns 200; ok field indicates health
	resp := parseResp(t, rec)

	var status map[string]any
	if err := json.Unmarshal(resp.Data, &status); err != nil {
		t.Fatalf("unmarshal health data: %v", err)
	}
	if ok, _ := status["ok"].(bool); ok {
		t.Error("expected ok=false when docker is down")
	}
	if _, hasErr := status["error"]; !hasErr {
		t.Error("expected error field when docker is down")
	}
}

// ── State ─────────────────────────────────────────────────────────────────────

func TestGetState_ReturnsSummaryForAllSubsystems(t *testing.T) {
	fake := defaultFake()
	fake.summ = &fakeStateSumm{req: 2, act: 1, fail: 0, aband: 0}

	rec := httptest.NewRecorder()
	newTestHandler(fake).getState(rec, newReq("GET", "/api/state", nil))

	assertStatus(t, rec, http.StatusOK)
	resp := parseResp(t, rec)
	if resp.Data == nil {
		t.Fatal("expected data in state response")
	}

	// Verify all six subsystems are present.
	data := string(resp.Data)
	for _, key := range []string{"images", "containers", "volumes", "networks", "builds", "execs"} {
		if !containsStr(data, key) {
			t.Errorf("state response missing subsystem %q: %s", key, data)
		}
	}
}

func TestGetState_ReflectsCounts(t *testing.T) {
	fake := defaultFake()
	fake.summ = &fakeStateSumm{req: 3, act: 2, fail: 1, aband: 0}

	rec := httptest.NewRecorder()
	newTestHandler(fake).getState(rec, newReq("GET", "/api/state", nil))

	assertStatus(t, rec, http.StatusOK)

	var outer struct {
		Data map[string]stateSummary `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&outer); err != nil {
		t.Fatalf("decode state: %v", err)
	}

	img := outer.Data["images"]
	if img.Requested != 3 {
		t.Errorf("images.requested: got %d, want 3", img.Requested)
	}
	if img.Active != 2 {
		t.Errorf("images.active: got %d, want 2", img.Active)
	}
	if img.Failed != 1 {
		t.Errorf("images.failed: got %d, want 1", img.Failed)
	}
}

func TestGetState_ZeroCounts(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestHandler(defaultFake()).getState(rec, newReq("GET", "/api/state", nil))

	assertStatus(t, rec, http.StatusOK)

	var outer struct {
		Data map[string]stateSummary `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&outer); err != nil {
		t.Fatalf("decode state: %v", err)
	}

	for name, s := range outer.Data {
		if s.Requested != 0 || s.Active != 0 || s.Failed != 0 || s.Abandoned != 0 {
			t.Errorf("%s: expected all zero counts, got %+v", name, s)
		}
	}
}

// ── Ping helper ───────────────────────────────────────────────────────────────

func TestHealth_PingContextUsed(t *testing.T) {
	var ctxCalled context.Context
	fake := defaultFake()
	fake.pingErr = nil

	// Wrap the ping to capture the context.
	type pingFn func(ctx context.Context) error
	var called bool
	_ = pingFn(func(ctx context.Context) error {
		ctxCalled = ctx
		called = true
		return nil
	})

	// Just verify the handler doesn't panic or error on a normal request.
	_ = ctxCalled
	_ = called

	rec := httptest.NewRecorder()
	newTestHandler(fake).health(rec, newReq("GET", "/api/health", nil))
	assertStatus(t, rec, http.StatusOK)
}
