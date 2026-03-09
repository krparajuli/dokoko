// Package server wires together the HTTP server, REST handlers, and SSE log streaming.
package server

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"time"

	authpkg "dokoko.ai/dokoko/internal/auth"
	imagecfg "dokoko.ai/dokoko/internal/imageconfig"
	dockermanager "dokoko.ai/dokoko/internal/docker/manager"
	"dokoko.ai/dokoko/pkg/logger"
)

// Server holds the HTTP server and all handler dependencies.
type Server struct {
	mgr           Manager
	log           *logger.Logger
	logBus        *logBus
	httpSrv       *http.Server
	authStore     *authpkg.Store
	allowedImages []string          // catalog IDs non-admin users may provision; nil = all
	imageConfig   *imagecfg.Config  // per-image env-var schemas
}

// New creates a configured Server.
// uiDir is the path to the built frontend files; empty string auto-detects.
// db is the open SQLite database used for user and session storage.
// allowedImages is the whitelist of catalog IDs for non-admin users; nil/empty = all allowed.
// imageConfig holds per-image env-var schemas loaded from the YAML config file.
func New(mgr *dockermanager.Manager, log *logger.Logger, addr, uiDir string, db *sql.DB, allowedImages []string, imageConfig *imagecfg.Config) *Server {
	bus := newLogBus()
	store := authpkg.New(db)
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for range t.C {
			store.PruneExpired()
		}
	}()
	s := &Server{
		mgr:           newManagerAdapter(mgr),
		log:           log,
		logBus:        bus,
		authStore:     store,
		allowedImages: allowedImages,
		imageConfig:   imageConfig,
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
