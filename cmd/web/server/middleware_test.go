package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ── CORS ──────────────────────────────────────────────────────────────────────

func TestCORS_SetsHeadersOnGET(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := cors(inner)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/health", nil))

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Allow-Origin: got %q, want *", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Error("Allow-Methods header not set")
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got == "" {
		t.Error("Allow-Headers header not set")
	}
}

func TestCORS_OptionsPreflightReturns204(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Should not be reached for OPTIONS.
		w.WriteHeader(http.StatusTeapot)
	})
	handler := cors(inner)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("OPTIONS", "/api/images", nil))

	if rec.Code != http.StatusNoContent {
		t.Errorf("OPTIONS: got %d, want 204", rec.Code)
	}
}

func TestCORS_DoesNotBlockPOST(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	handler := cors(inner)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("POST", "/api/images/pull", nil))

	if rec.Code != http.StatusAccepted {
		t.Errorf("POST: got %d, want 202", rec.Code)
	}
}

func TestCORS_HeadersPresentOnDELETE(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	handler := cors(inner)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/containers/abc", nil))

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Allow-Origin on DELETE: got %q, want *", got)
	}
}
