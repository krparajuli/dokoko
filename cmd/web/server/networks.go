package server

import (
	"net/http"

	dockertypes "github.com/docker/docker/api/types"
	dockerfilters "github.com/docker/docker/api/types/filters"
)

// listNetworks returns all networks from the in-memory store.
func (h *handler) listNetworks(w http.ResponseWriter, r *http.Request) {
	records := h.mgr.Networks().Store().All()
	jsonOK(w, records)
}

// createNetwork creates a new Docker network.
// Body: {"name":"my-net","driver":"bridge"}
func (h *handler) createNetwork(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string `json:"name"`
		Driver string `json:"driver"`
	}
	if err := decode(r, &body); err != nil || body.Name == "" {
		jsonErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if body.Driver == "" {
		body.Driver = "bridge"
	}
	ctx, cancel := opCtx(r)
	defer cancel()

	if _, err := h.mgr.Networks().Create(ctx, body.Name, dockertypes.NetworkCreate{
		Driver: body.Driver,
	}); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonAccepted(w, "network created: "+body.Name)
}

// removeNetwork removes a network by ID or name.
func (h *handler) removeNetwork(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx, cancel := opCtx(r)
	defer cancel()

	if _, err := h.mgr.Networks().Remove(ctx, id); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonAccepted(w, "network removed: "+id)
}

// pruneNetworks removes all unused networks.
func (h *handler) pruneNetworks(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := opCtx(r)
	defer cancel()

	if _, err := h.mgr.Networks().Prune(ctx, dockerfilters.Args{}); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonAccepted(w, "network prune dispatched")
}

// refreshNetworks syncs the network store with live Docker data.
func (h *handler) refreshNetworks(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := opCtx(r)
	defer cancel()

	if err := h.mgr.Networks().Refresh(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, "network store refreshed")
}

// inspectNetwork inspects a network by ID or name.
func (h *handler) inspectNetwork(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx, cancel := opCtx(r)
	defer cancel()

	res := <-h.mgr.Networks().Inspect(ctx, id, dockertypes.NetworkInspectOptions{})
	if res.Err != nil {
		jsonErr(w, http.StatusInternalServerError, res.Err.Error())
		return
	}
	jsonOK(w, res.Network)
}
