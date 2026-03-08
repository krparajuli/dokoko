package server

import (
	"fmt"
	"net"
	"net/http"

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

	// Return the session so the frontend can build the terminal URL immediately.
	sess := wc.GetSession(body.UserID)
	if sess == nil {
		jsonErr(w, http.StatusInternalServerError, "session not found after provision")
		return
	}
	jsonOK(w, sessionResponse(r, sess))
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
	jsonOK(w, sessionResponse(r, sess))
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

// ── Helpers ───────────────────────────────────────────────────────────────────

type sessionResp struct {
	UserID        string `json:"user_id"`
	CatalogID     string `json:"catalog_id"`
	ContainerName string `json:"container_name"`
	ContainerID   string `json:"container_id"`
	Status        string `json:"status"`
	ErrorMsg      string `json:"error,omitempty"`
	TerminalURL   string `json:"terminal_url,omitempty"`
}

// sessionResponse builds the JSON-safe representation of a UserSession.
// terminal_url is populated only when the session is ready; its hostname is
// derived from the request so remote clients get the right address.
func sessionResponse(r *http.Request, sess *webcontainersstate.UserSession) sessionResp {
	resp := sessionResp{
		UserID:        sess.UserID,
		CatalogID:     sess.CatalogID,
		ContainerName: sess.ContainerName,
		ContainerID:   sess.ContainerID,
		Status:        string(sess.Status),
		ErrorMsg:      sess.ErrorMsg,
	}
	if sess.Status == webcontainersstate.StatusReady && sess.HostPort > 0 {
		host := requestHost(r)
		resp.TerminalURL = fmt.Sprintf("http://%s:%d/", host, sess.HostPort)
	}
	return resp
}

// requestHost returns just the hostname from the HTTP request's Host header,
// stripping any port.  Falls back to "localhost".
func requestHost(r *http.Request) string {
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		if host, _, err := net.SplitHostPort(h); err == nil {
			return host
		}
		return h
	}
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		if r.Host != "" {
			return r.Host
		}
		return "localhost"
	}
	return host
}
