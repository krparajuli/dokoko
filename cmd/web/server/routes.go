package server

import (
	"net/http"
	"os"
	"path/filepath"
)

func (s *Server) routes(uiDir string) http.Handler {
	mux := http.NewServeMux()
	h := &handler{mgr: s.mgr, log: s.log, authStore: s.authStore, allowedImages: s.allowedImages}

	// Auth routes (no authentication required)
	mux.HandleFunc("POST /api/auth/login", h.loginHandler)
	mux.HandleFunc("POST /api/auth/logout", h.logoutHandler)
	mux.HandleFunc("GET /api/auth/me", h.meHandler)

	// Health / connection
	mux.HandleFunc("GET /api/health", h.health)

	// Images
	mux.HandleFunc("GET /api/images", h.listImages)
	mux.HandleFunc("POST /api/images/pull", h.pullImage)
	mux.HandleFunc("POST /api/images/remove", h.removeImage)
	mux.HandleFunc("POST /api/images/tag", h.tagImage)
	mux.HandleFunc("POST /api/images/refresh", h.refreshImages)
	mux.HandleFunc("GET /api/images/{id}/inspect", h.inspectImage)

	// Containers
	mux.HandleFunc("GET /api/containers", h.listContainers)
	mux.HandleFunc("POST /api/containers", h.createContainer)
	mux.HandleFunc("POST /api/containers/{id}/start", h.startContainer)
	mux.HandleFunc("POST /api/containers/{id}/stop", h.stopContainer)
	mux.HandleFunc("DELETE /api/containers/{id}", h.removeContainer)
	mux.HandleFunc("GET /api/containers/{id}/inspect", h.inspectContainer)

	// Volumes
	mux.HandleFunc("GET /api/volumes", h.listVolumes)
	mux.HandleFunc("POST /api/volumes", h.createVolume)
	mux.HandleFunc("DELETE /api/volumes/{name}", h.removeVolume)
	mux.HandleFunc("POST /api/volumes/prune", h.pruneVolumes)
	mux.HandleFunc("POST /api/volumes/refresh", h.refreshVolumes)
	mux.HandleFunc("GET /api/volumes/{name}/inspect", h.inspectVolume)

	// Networks
	mux.HandleFunc("GET /api/networks", h.listNetworks)
	mux.HandleFunc("POST /api/networks", h.createNetwork)
	mux.HandleFunc("DELETE /api/networks/{id}", h.removeNetwork)
	mux.HandleFunc("POST /api/networks/prune", h.pruneNetworks)
	mux.HandleFunc("POST /api/networks/refresh", h.refreshNetworks)
	mux.HandleFunc("GET /api/networks/{id}/inspect", h.inspectNetwork)

	// Execs
	mux.HandleFunc("POST /api/execs", h.createExec)
	mux.HandleFunc("POST /api/execs/{id}/start", h.startExec)
	mux.HandleFunc("GET /api/execs/{id}/inspect", h.inspectExec)

	// User management (admin only)
	mux.HandleFunc("GET /api/users", h.listUsers)
	mux.HandleFunc("POST /api/users", h.createUser)
	mux.HandleFunc("DELETE /api/users/{username}", h.deleteUser)
	mux.HandleFunc("PUT /api/users/{username}/password", h.updateUserPassword)

	// Web Containers (user terminal sessions)
	mux.HandleFunc("GET /api/webcontainers/catalog", h.listWebCatalog)
	mux.HandleFunc("POST /api/webcontainers/provision", h.provisionWebContainer)
	mux.HandleFunc("GET /api/webcontainers/session/{user_id}", h.getWebSession)
	mux.HandleFunc("DELETE /api/webcontainers/session/{user_id}", h.terminateWebSession)
	mux.HandleFunc("GET /api/webcontainers/env/{user_id}", h.getContainerEnv)
	mux.HandleFunc("PUT /api/webcontainers/env/{user_id}", h.setContainerEnv)
	// Subtree pattern — catches all ttyd sub-paths (assets, /ws, /token).
	mux.HandleFunc("/api/webcontainers/terminal/{user_id}/", h.proxyWebTerminal)

	// ProxyPortMap (dynamic port scanning + proxy registration)
	mux.HandleFunc("POST /api/proxyportmap/scan", h.scanPorts)
	mux.HandleFunc("GET /api/proxyportmap/mappings/{user_id}", h.getMappings)
	mux.HandleFunc("DELETE /api/proxyportmap/mappings/{user_id}", h.unmapPorts)
	// Subtree — proxies user container ports through the web server (same-origin).
	mux.HandleFunc("/api/webcontainers/port/{user_id}/{container_port}/", h.proxyUserPort)

	// State (all subsystems)
	mux.HandleFunc("GET /api/state", h.getState)

	// SSE log stream
	mux.HandleFunc("GET /api/logs/stream", s.streamLogs)

	// Static UI files
	dir := resolveUIDir(uiDir)
	if dir != "" {
		mux.Handle("/", http.FileServer(http.Dir(dir)))
	}

	return cors(authMiddleware(s.authStore, s.log)(mux))
}

// resolveUIDir returns the UI dist directory path.
// Tries uiDir hint first, then looks for ui/dist relative to cwd.
func resolveUIDir(hint string) string {
	if hint != "" {
		if _, err := os.Stat(hint); err == nil {
			return hint
		}
	}
	// Try relative to cwd (works when running from cmd/web/)
	candidates := []string{
		"ui/dist",
		filepath.Join("cmd", "web", "ui", "dist"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}
