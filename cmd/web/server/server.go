// Package server wires together the HTTP server, REST handlers, and SSE log streaming.
package server

import (
	"context"
	"io"
	"net/http"
	"time"

	authpkg "dokoko.ai/dokoko/internal/auth"
	dockermanager "dokoko.ai/dokoko/internal/docker/manager"
	"dokoko.ai/dokoko/pkg/logger"
)

// Server holds the HTTP server and all handler dependencies.
type Server struct {
	mgr       Manager
	log       *logger.Logger
	logBus    *logBus
	httpSrv   *http.Server
	authStore *authpkg.Store
}

// New creates a configured Server.
// uiDir is the path to the built frontend files; empty string auto-detects.
// authUsers is the list of users for authentication.
func New(mgr *dockermanager.Manager, log *logger.Logger, addr, uiDir string, authUsers []authpkg.User) *Server {
	bus := newLogBus()
	store := authpkg.NewStore(authUsers)
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for range t.C {
			store.PruneExpired()
		}
	}()
	s := &Server{
		mgr:       newManagerAdapter(mgr),
		log:       log,
		logBus:    bus,
		authStore: store,
	}
	s.httpSrv = &http.Server{
		Addr:    addr,
		Handler: s.routes(uiDir),
	}
	return s
}

// LogWriter returns an io.Writer that feeds log lines to all SSE clients.
func (s *Server) LogWriter() io.Writer { return s.logBus }

// Start begins serving HTTP requests.
func (s *Server) Start() error { return s.httpSrv.ListenAndServe() }

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error { return s.httpSrv.Shutdown(ctx) }
