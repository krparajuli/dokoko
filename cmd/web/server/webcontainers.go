package server

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	authpkg "dokoko.ai/dokoko/internal/auth"
	webcontainersstate "dokoko.ai/dokoko/internal/webcontainers/state"
)

// nerdFontCSS is injected into every ttyd HTML response so the xterm.js
// terminal can render Nerd Font icons, powerline symbols, and box-drawing
// characters correctly.  The font is loaded from jsDelivr's GitHub CDN,
// which caches and serves the official nerd-fonts repository assets.
const nerdFontCSS = `<style>
@font-face {
    font-family: "JetBrainsMono Nerd Font Mono";
    src: url("https://cdn.jsdelivr.net/gh/ryanoasis/nerd-fonts@v3.3.0/patched-fonts/JetBrainsMono/Mono/JetBrainsMonoNLNerdFontMono-Regular.ttf") format("truetype");
    font-weight: normal;
    font-style: normal;
    font-display: swap;
}
@font-face {
    font-family: "JetBrainsMono Nerd Font Mono";
    src: url("https://cdn.jsdelivr.net/gh/ryanoasis/nerd-fonts@v3.3.0/patched-fonts/JetBrainsMono/Mono/JetBrainsMonoNLNerdFontMono-Bold.ttf") format("truetype");
    font-weight: bold;
    font-style: normal;
    font-display: swap;
}
</style>`

func joinStrings(ss []string) string { return strings.Join(ss, ", ") }

// ── Image variable schema ─────────────────────────────────────────────────────

// getImageVars returns the environment-variable schema for a catalog image.
//
// GET /api/webcontainers/imagevars/{catalog_id}
func (h *handler) getImageVars(w http.ResponseWriter, r *http.Request) {
	catalogID := r.PathValue("catalog_id")
	type varResp struct {
		Name         string `json:"name"`
		Required     bool   `json:"required"`
		HasDefault   bool   `json:"has_default"`
		DefaultValue string `json:"default_value"`
	}
	vars := h.imageConfig.FindVars(catalogID)
	out := make([]varResp, len(vars))
	for i, v := range vars {
		out[i] = varResp{
			Name:         v.Name,
			Required:     v.Required,
			HasDefault:   v.HasDefault,
			DefaultValue: v.DefaultValue,
		}
	}
	jsonOK(w, map[string]any{"vars": out})
}

// ── Catalog ───────────────────────────────────────────────────────────────────

// listWebCatalog returns the ordered list of approved container images.
// Non-admin users only see images on the --allowed-images whitelist (if set).
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

	sess, _ := sessionFromContext(r.Context())
	defs := wc.Catalog()
	out := make([]catalogEntry, 0, len(defs))
	for _, d := range defs {
		if sess != nil && sess.Role != authpkg.RoleAdmin && !h.imageAllowedForUser(d.ID) {
			continue
		}
		out = append(out, catalogEntry{
			ID:          d.ID,
			Image:       d.Image,
			DisplayName: d.DisplayName,
			Description: d.Description,
		})
	}
	jsonOK(w, out)
}

// imageAllowedForUser reports whether a catalog ID is on the allowed-images
// whitelist.  If the whitelist is empty every image is allowed.
func (h *handler) imageAllowedForUser(catalogID string) bool {
	if len(h.allowedImages) == 0 {
		return true
	}
	for _, id := range h.allowedImages {
		if id == catalogID {
			return true
		}
	}
	return false
}

// ── Provision ─────────────────────────────────────────────────────────────────

// provisionWebContainer asynchronously provisions a web-terminal container.
// If the user already has a running container it is reused.
//
// Non-admin users: must use a whitelisted catalog ID and may only hold one
// active session at a time.
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

	// Enforce whitelist and single-container limit for non-admin users.
	if sess, ok := sessionFromContext(r.Context()); ok && sess.Role != authpkg.RoleAdmin {
		if !h.imageAllowedForUser(body.CatalogID) {
			jsonErr(w, http.StatusForbidden, "image not available for your account")
			return
		}
		existing := wc.GetSession(body.UserID)
		if existing != nil &&
			existing.Status != webcontainersstate.StatusStopped &&
			existing.Status != webcontainersstate.StatusError {
			jsonErr(w, http.StatusConflict, "you already have an active container — terminate it first")
			return
		}
	}

	ctx, cancel := opCtx(r)
	defer cancel()

	// Apply image-var schema: fill defaults, then enforce required vars.
	stored := wc.GetEnvVars(body.UserID)
	merged := h.imageConfig.ApplyDefaults(body.CatalogID, stored)
	if missing := h.imageConfig.MissingRequired(body.CatalogID, merged); len(missing) > 0 {
		jsonErr(w, http.StatusUnprocessableEntity,
			"required environment variable(s) not set: "+joinStrings(missing))
		return
	}
	// Persist any newly-applied defaults so they survive future provisions.
	if len(merged) != len(stored) {
		if err := wc.SetEnvVars(ctx, body.UserID, merged); err != nil {
			h.log.Warn("provision: failed to persist default env vars for %s: %v", body.UserID, err)
		}
	}

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

// ── Environment variables ─────────────────────────────────────────────────────

// getContainerEnv returns the stored env vars for the given user.
//
// GET /api/webcontainers/env/{user_id}
func (h *handler) getContainerEnv(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("user_id")
	wc := h.mgr.WebContainers()
	if wc == nil {
		jsonErr(w, http.StatusServiceUnavailable, "webcontainers not available")
		return
	}
	vars := wc.GetEnvVars(userID)
	if vars == nil {
		vars = map[string]string{}
	}
	jsonOK(w, vars)
}

// setContainerEnv replaces all env vars for the user and applies them live
// to the running container (if one is ready).
//
// POST /api/webcontainers/env/{user_id}
// Body: {"KEY":"VALUE", ...}
func (h *handler) setContainerEnv(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("user_id")
	wc := h.mgr.WebContainers()
	if wc == nil {
		jsonErr(w, http.StatusServiceUnavailable, "webcontainers not available")
		return
	}

	var vars map[string]string
	if err := decode(r, &vars); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	ctx, cancel := opCtx(r)
	defer cancel()

	if err := wc.SetEnvVars(ctx, userID, vars); err != nil {
		jsonErr(w, http.StatusInternalServerError, "apply env vars: "+err.Error())
		return
	}
	jsonOK(w, vars)
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
		// Remove Accept-Encoding so ttyd always returns uncompressed responses,
		// which lets ModifyResponse splice in the NerdFont CSS without having to
		// deal with gzip/zstd decoding.
		req.Header.Del("Accept-Encoding")
	}

	// Inject NerdFont CSS into every HTML response so xterm.js can render
	// Nerd Font glyphs, powerline symbols, and box-drawing characters.
	proxy.ModifyResponse = func(resp *http.Response) error {
		if !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
			return nil
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}
		body = bytes.Replace(body, []byte("</head>"), []byte(nerdFontCSS+"</head>"), 1)
		resp.Body = io.NopCloser(bytes.NewReader(body))
		resp.ContentLength = int64(len(body))
		resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
		return nil
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
