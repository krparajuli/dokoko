package server

import (
	"net/http"

	dockerfilters "github.com/docker/docker/api/types/filters"
	dockervolume "github.com/docker/docker/api/types/volume"
)

// listVolumes returns all volumes from the in-memory store.
func (h *handler) listVolumes(w http.ResponseWriter, r *http.Request) {
	records := h.mgr.Volumes().Store().All()
	jsonOK(w, records)
}

// createVolume creates a new Docker volume.
// Body: {"name":"my-vol","driver":"local"}
func (h *handler) createVolume(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string `json:"name"`
		Driver string `json:"driver"`
	}
	if err := decode(r, &body); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Driver == "" {
		body.Driver = "local"
	}
	ctx, cancel := opCtx(r)
	defer cancel()

	ticket, err := h.mgr.Volumes().Create(ctx, dockervolume.CreateOptions{
		Name:   body.Name,
		Driver: body.Driver,
	})
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := ticket.Wait(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonAccepted(w, "volume created: "+body.Name)
}

// removeVolume removes a volume by name.
func (h *handler) removeVolume(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ctx, cancel := opCtx(r)
	defer cancel()

	ticket, err := h.mgr.Volumes().Remove(ctx, name, false)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := ticket.Wait(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonAccepted(w, "volume removed: "+name)
}

// pruneVolumes removes all unused volumes.
func (h *handler) pruneVolumes(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := opCtx(r)
	defer cancel()

	ticket, err := h.mgr.Volumes().Prune(ctx, dockerfilters.Args{})
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := ticket.Wait(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonAccepted(w, "volume prune dispatched")
}

// refreshVolumes syncs the volume store with live Docker data.
func (h *handler) refreshVolumes(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := opCtx(r)
	defer cancel()

	if err := h.mgr.Volumes().Refresh(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, "volume store refreshed")
}

// inspectVolume inspects a volume by name.
func (h *handler) inspectVolume(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ctx, cancel := opCtx(r)
	defer cancel()

	res := <-h.mgr.Volumes().Inspect(ctx, name)
	if res.Err != nil {
		jsonErr(w, http.StatusInternalServerError, res.Err.Error())
		return
	}
	jsonOK(w, res.Volume)
}
