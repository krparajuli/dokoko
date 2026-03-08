package server

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
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

// ── Port proxy ────────────────────────────────────────────────────────────────

// proxyUserPort reverse-proxies traffic for a mapped container port through
// the dokoko web server so the browser never needs a separate host:port.
//
// Subtree pattern — all sub-paths are forwarded to nginx → user container:
//
//	/api/webcontainers/port/{user_id}/{container_port}/
//	/api/webcontainers/port/{user_id}/{container_port}/index.html
func (h *handler) proxyUserPort(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("user_id")
	cpStr := r.PathValue("container_port")

	n, err := strconv.ParseUint(cpStr, 10, 16)
	if err != nil {
		http.Error(w, "invalid container_port", http.StatusBadRequest)
		return
	}
	containerPort := uint16(n)

	ppm := h.mgr.ProxyPortMap()
	if ppm == nil {
		http.Error(w, "proxyportmap not available", http.StatusServiceUnavailable)
		return
	}

	result := ppm.GetResult(userID)
	if result == nil {
		http.Error(w, "no port mapping for user — run scan first", http.StatusNotFound)
		return
	}

	var hostPort uint16
	for _, p := range result.Ports {
		if p.ContainerPort == containerPort {
			hostPort = p.HostPort
			break
		}
	}
	if hostPort == 0 {
		http.Error(w, fmt.Sprintf("port %d not mapped for user", containerPort), http.StatusNotFound)
		return
	}

	target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", hostPort))
	proxy := httputil.NewSingleHostReverseProxy(target)

	// Strip the dokoko path prefix before forwarding so the upstream app
	// sees its own path space (e.g. "/" not "/api/webcontainers/port/…/8080/").
	prefix := fmt.Sprintf("/api/webcontainers/port/%s/%d", userID, containerPort)
	defaultDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		defaultDirector(req)
		req.URL.Path = strings.TrimPrefix(req.URL.Path, prefix)
		if req.URL.Path == "" || req.URL.Path[0] != '/' {
			req.URL.Path = "/" + req.URL.Path
		}
		req.Host = target.Host
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		h.log.Warn("proxyUserPort: upstream error user=%s port=%d: %v", userID, containerPort, err)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}

	proxy.ServeHTTP(w, r)
}

// ── Response helpers ──────────────────────────────────────────────────────────

type portEntryResp struct {
	ContainerPort uint16 `json:"container_port"`
	HostPort      uint16 `json:"host_port"`
	URL           string `json:"url"`
	Process       string `json:"process,omitempty"`
}

type portScanResp struct {
	UserID        string          `json:"user_id"`
	ContainerName string          `json:"container_name,omitempty"`
	Ports         []portEntryResp `json:"ports"`
	ScannedAt     time.Time       `json:"scanned_at"`
}

// buildPortScanResp converts a ScanResult to its JSON representation.
// URLs are same-origin paths routed through the dokoko web server proxy so
// the browser never needs to connect to a raw host:port.
func buildPortScanResp(_ *http.Request, result *proxyportmapstate.ScanResult) portScanResp {
	entries := make([]portEntryResp, 0, len(result.Ports))
	for _, p := range result.Ports {
		entries = append(entries, portEntryResp{
			ContainerPort: p.ContainerPort,
			HostPort:      p.HostPort,
			URL: fmt.Sprintf("/api/webcontainers/port/%s/%d/",
				result.UserID, p.ContainerPort),
			Process: p.Process,
		})
	}
	return portScanResp{
		UserID:        result.UserID,
		ContainerName: result.ContainerName,
		Ports:         entries,
		ScannedAt:     result.ScannedAt,
	}
}
