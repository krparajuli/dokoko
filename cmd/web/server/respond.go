package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	authpkg "dokoko.ai/dokoko/internal/auth"
	"dokoko.ai/dokoko/pkg/logger"
)

// handler holds shared dependencies for all HTTP handlers.
type handler struct {
	mgr           Manager
	log           *logger.Logger
	authStore     *authpkg.Store
	allowedImages []string // catalog IDs non-admin users may provision; nil = all
}

// jsonOK writes a 200 JSON response with the given payload.
func jsonOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]any{"data": data}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// jsonAccepted writes a 202 JSON response for async dispatched operations.
func jsonAccepted(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"message": msg})
}

// jsonErr writes a JSON error response with the given status code.
func jsonErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

// decode reads and decodes a JSON request body into v.
func decode(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// opCtx returns a context with a 30-second timeout for Docker operations.
// It is intentionally decoupled from r.Context() so that async workers are
// not cancelled when the HTTP response is written and the request ends.
func opCtx(_ *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}
