package server

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"

	webcontainersstate "dokoko.ai/dokoko/internal/webcontainers/state"
)

// ── Catalog ───────────────────────────────────────────────────────────────────

// listWebCatalog returns the ordered list of approved container images.
//
// GET /api/webcontainers/catalog
func (h *handler) listWebCatalog(w http.ResponseWriter, r *http.Request) {
	wc := h.mgr.WebContainers()
	if wc == nil {
		jsonErr(w, http.StatusServiceUnavailable, "webcontainers not available")
		return
	}
	type catalogEntry struct {
		ID          string `json:"id"`
		Image       string `json:"image"`
		DisplayName string `json:"display_name"`
		Description string `json:"description"`
	}
	defs := wc.Catalog()
	out := make([]catalogEntry, 0, len(defs))
	for _, d := range defs {
		out = append(out, catalogEntry{
			ID:          d.ID,
			Image:       d.Image,
			DisplayName: d.DisplayName,
			Description: d.Description,
		})
	}
	jsonOK(w, out)
}

// ── Provision ─────────────────────────────────────────────────────────────────

// provisionWebContainer asynchronously provisions a web-terminal container.
// If the user already has a running container it is reused.
//
// POST /api/webcontainers/provision
// Body: {"user_id":"alice","catalog_id":"ubuntu"}
func (h *handler) provisionWebContainer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserID    string `json:"user_id"`
		CatalogID string `json:"catalog_id"`
	}
	if err := decode(r, &body); err != nil || body.UserID == "" || body.CatalogID == "" {
		jsonErr(w, http.StatusBadRequest, "user_id and catalog_id are required")
		return
	}

	wc := h.mgr.WebContainers()
	if wc == nil {
		jsonErr(w, http.StatusServiceUnavailable, "webcontainers not available")
		return
	}

	ctx, cancel := opCtx(r)
	defer cancel()

	ticket, err := wc.Provision(ctx, body.UserID, body.CatalogID)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := ticket.Wait(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "provision did not complete: "+err.Error())
		return
	}

	sess := wc.GetSession(body.UserID)
	if sess == nil {
		jsonErr(w, http.StatusInternalServerError, "session not found after provision")
		return
	}
	jsonOK(w, sessionResponse(sess))
}

// ── Session ───────────────────────────────────────────────────────────────────

// getWebSession returns the current session for the given user ID.
//
// GET /api/webcontainers/session/{user_id}
func (h *handler) getWebSession(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("user_id")
	wc := h.mgr.WebContainers()
	if wc == nil {
		jsonErr(w, http.StatusServiceUnavailable, "webcontainers not available")
		return
	}
	sess := wc.GetSession(userID)
	if sess == nil {
		jsonErr(w, http.StatusNotFound, "no session found for user")
		return
	}
	jsonOK(w, sessionResponse(sess))
}

// terminateWebSession stops and removes the user's web container.
//
// DELETE /api/webcontainers/session/{user_id}
func (h *handler) terminateWebSession(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("user_id")
	wc := h.mgr.WebContainers()
	if wc == nil {
		jsonErr(w, http.StatusServiceUnavailable, "webcontainers not available")
		return
	}

	ctx, cancel := opCtx(r)
	defer cancel()

	ticket, err := wc.Terminate(ctx, userID)
	if err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}
	if err := ticket.Wait(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "terminate did not complete: "+err.Error())
		return
	}
	jsonAccepted(w, "session terminated for user: "+userID)
}

// ── Terminal proxy ────────────────────────────────────────────────────────────

// proxyWebTerminal reverse-proxies all ttyd traffic (HTTP assets + WebSocket)
// for the given user through the dokoko web server so the browser never needs
// to connect to a separate port.
//
// The path `/api/webcontainers/terminal/{user_id}/` is registered as a
// subtree pattern, so it catches every sub-path:
//   - GET  /…/{user_id}/          → ttyd HTML page
//   - GET  /…/{user_id}/token     → ttyd auth token
//   - GET  /…/{user_id}/ws        → WebSocket terminal (upgraded by ReverseProxy)
//
// ttyd is started with --base-path matching this prefix so it generates
// correct relative URLs for its static assets.
func (h *handler) proxyWebTerminal(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("user_id")

	wc := h.mgr.WebContainers()
	if wc == nil {
		http.Error(w, "webcontainers not available", http.StatusServiceUnavailable)
		return
	}

	sess := wc.GetSession(userID)
	if sess == nil || sess.Status != webcontainersstate.StatusReady || sess.HostPort == 0 {
		http.Error(w, "no ready session for this user", http.StatusNotFound)
		return
	}

	target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", sess.HostPort))
	proxy := httputil.NewSingleHostReverseProxy(target)

	// Override the Director so the request path is forwarded as-is.
	// httputil.NewSingleHostReverseProxy's default Director prepends the
	// target's path (empty here), which is what we want; the only correction
	// needed is clearing the Host header so ttyd receives the backend address.
	defaultDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		defaultDirector(req)
		req.Host = target.Host
	}

	// Suppress default error logging; surface errors as plain 502s instead.
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		h.log.Warn("webcontainer proxy: upstream error user=%s: %v", userID, err)
		http.Error(w, "terminal unavailable — ttyd may still be starting", http.StatusBadGateway)
	}

	proxy.ServeHTTP(w, r)
}

// ── Session response ──────────────────────────────────────────────────────────

type sessionResp struct {
	UserID        string `json:"user_id"`
	CatalogID     string `json:"catalog_id"`
	ContainerName string `json:"container_name"`
	ContainerID   string `json:"container_id"`
	Status        string `json:"status"`
	ErrorMsg      string `json:"error,omitempty"`
	// TerminalPath is the same-origin path to the embedded terminal.
	// Only present when Status == "ready".
	TerminalPath string `json:"terminal_path,omitempty"`
}

func sessionResponse(sess *webcontainersstate.UserSession) sessionResp {
	resp := sessionResp{
		UserID:        sess.UserID,
		CatalogID:     sess.CatalogID,
		ContainerName: sess.ContainerName,
		ContainerID:   sess.ContainerID,
		Status:        string(sess.Status),
		ErrorMsg:      sess.ErrorMsg,
	}
	if sess.Status == webcontainersstate.StatusReady && sess.HostPort > 0 {
		resp.TerminalPath = "/api/webcontainers/terminal/" + sess.UserID + "/"
	}
	return resp
}
