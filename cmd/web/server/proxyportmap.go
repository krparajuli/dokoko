package server

import (
	"fmt"
	"net/http"
	"time"

	proxyportmapstate "dokoko.ai/dokoko/internal/proxyportmap/state"
)

// ── scanPorts ─────────────────────────────────────────────────────────────────

// scanPorts scans the user's webcontainer for listening TCP ports, registers
// them with the nginx proxy, and returns the resulting port mapping.
//
// POST /api/proxyportmap/scan
// Body: {"user_id":"user-abc123"}
func (h *handler) scanPorts(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserID string `json:"user_id"`
	}
	if err := decode(r, &body); err != nil || body.UserID == "" {
		jsonErr(w, http.StatusBadRequest, "user_id is required")
		return
	}

	ppm := h.mgr.ProxyPortMap()
	if ppm == nil {
		jsonErr(w, http.StatusServiceUnavailable, "proxyportmap not available")
		return
	}

	// Look up the user's running webcontainer to get container name and ID.
	wc := h.mgr.WebContainers()
	if wc == nil {
		jsonErr(w, http.StatusServiceUnavailable, "webcontainers not available")
		return
	}
	sess := wc.GetSession(body.UserID)
	if sess == nil {
		jsonErr(w, http.StatusNotFound, "no active webcontainer session for user")
		return
	}

	ctx, cancel := opCtx(r)
	defer cancel()

	ticket, err := ppm.ScanAndMap(ctx, body.UserID, sess.ContainerName, sess.ContainerID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "scan failed: "+err.Error())
		return
	}
	if err := ticket.Wait(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "scan did not complete: "+err.Error())
		return
	}

	result := ppm.GetResult(body.UserID)
	if result == nil {
		jsonOK(w, portScanResp{
			UserID:    body.UserID,
			Ports:     []portEntryResp{},
			ScannedAt: time.Now(),
		})
		return
	}
	jsonOK(w, buildPortScanResp(r, result))
}

// ── getMappings ───────────────────────────────────────────────────────────────

// getMappings returns the current port-mapping result for the given user.
//
// GET /api/proxyportmap/mappings/{user_id}
func (h *handler) getMappings(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("user_id")

	ppm := h.mgr.ProxyPortMap()
	if ppm == nil {
		jsonErr(w, http.StatusServiceUnavailable, "proxyportmap not available")
		return
	}

	result := ppm.GetResult(userID)
	if result == nil {
		jsonErr(w, http.StatusNotFound, "no port mapping found for user")
		return
	}
	jsonOK(w, buildPortScanResp(r, result))
}

// ── unmapPorts ────────────────────────────────────────────────────────────────

// unmapPorts deregisters the user's mapped ports from the nginx proxy.
//
// DELETE /api/proxyportmap/mappings/{user_id}
func (h *handler) unmapPorts(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("user_id")

	ppm := h.mgr.ProxyPortMap()
	if ppm == nil {
		jsonErr(w, http.StatusServiceUnavailable, "proxyportmap not available")
		return
	}

	ctx, cancel := opCtx(r)
	defer cancel()

	ticket, err := ppm.Unmap(ctx, userID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "unmap failed: "+err.Error())
		return
	}
	if err := ticket.Wait(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "unmap did not complete: "+err.Error())
		return
	}
	jsonAccepted(w, "ports unmapped for user: "+userID)
}

// ── Response helpers ──────────────────────────────────────────────────────────

type portEntryResp struct {
	ContainerPort uint16 `json:"container_port"`
	HostPort      uint16 `json:"host_port"`
	URL           string `json:"url"`
}

type portScanResp struct {
	UserID        string          `json:"user_id"`
	ContainerName string          `json:"container_name,omitempty"`
	Ports         []portEntryResp `json:"ports"`
	ScannedAt     time.Time       `json:"scanned_at"`
}

// buildPortScanResp converts a ScanResult to its JSON representation.
// The URL for each port uses the request's Host header so it works regardless
// of where the server is reachable (localhost, LAN IP, etc.).
func buildPortScanResp(r *http.Request, result *proxyportmapstate.ScanResult) portScanResp {
	host := r.Host
	// Strip any existing port from host for clean URL construction.
	entries := make([]portEntryResp, 0, len(result.Ports))
	for _, p := range result.Ports {
		entries = append(entries, portEntryResp{
			ContainerPort: p.ContainerPort,
			HostPort:      p.HostPort,
			URL:           fmt.Sprintf("http://%s:%d", hostOnly(host), p.HostPort),
		})
	}
	return portScanResp{
		UserID:        result.UserID,
		ContainerName: result.ContainerName,
		Ports:         entries,
		ScannedAt:     result.ScannedAt,
	}
}

// hostOnly strips the port from a host:port string, returning just the hostname.
func hostOnly(hostPort string) string {
	for i := len(hostPort) - 1; i >= 0; i-- {
		if hostPort[i] == ':' {
			return hostPort[:i]
		}
		if hostPort[i] == ']' { // IPv6 without port
			return hostPort
		}
	}
	return hostPort
}
