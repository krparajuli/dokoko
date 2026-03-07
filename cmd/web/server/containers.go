package server

import (
	"net/http"

	dockercontainer "github.com/docker/docker/api/types/container"
)

// listContainers returns all containers (including stopped).
func (h *handler) listContainers(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := opCtx(r)
	defer cancel()

	res := <-h.mgr.Containers().List(ctx, dockercontainer.ListOptions{All: true})
	if res.Err != nil {
		jsonErr(w, http.StatusInternalServerError, res.Err.Error())
		return
	}
	jsonOK(w, res.Containers)
}

// createContainer creates a container from an image.
// Body: {"image":"nginx","name":"my-nginx"}
func (h *handler) createContainer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Image string `json:"image"`
		Name  string `json:"name"`
	}
	if err := decode(r, &body); err != nil || body.Image == "" {
		jsonErr(w, http.StatusBadRequest, "image is required")
		return
	}
	ctx, cancel := opCtx(r)
	defer cancel()

	cfg := &dockercontainer.Config{Image: body.Image}
	ticket, err := h.mgr.Containers().Create(ctx, body.Name, cfg, nil, nil)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := ticket.Wait(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonAccepted(w, "container created (image="+body.Image+" name="+body.Name+")")
}

// startContainer starts a stopped container.
func (h *handler) startContainer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx, cancel := opCtx(r)
	defer cancel()

	ticket, err := h.mgr.Containers().Start(ctx, id, dockercontainer.StartOptions{})
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := ticket.Wait(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonAccepted(w, "start dispatched: "+id)
}

// stopContainer stops a running container.
func (h *handler) stopContainer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx, cancel := opCtx(r)
	defer cancel()

	ticket, err := h.mgr.Containers().Stop(ctx, id, dockercontainer.StopOptions{})
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := ticket.Wait(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonAccepted(w, "stop dispatched: "+id)
}

// removeContainer removes a container (forced).
func (h *handler) removeContainer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx, cancel := opCtx(r)
	defer cancel()

	ticket, err := h.mgr.Containers().Remove(ctx, id, dockercontainer.RemoveOptions{Force: true})
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := ticket.Wait(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonAccepted(w, "remove dispatched: "+id)
}

// inspectContainer inspects a single container.
func (h *handler) inspectContainer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx, cancel := opCtx(r)
	defer cancel()

	res := <-h.mgr.Containers().Inspect(ctx, id)
	if res.Err != nil {
		jsonErr(w, http.StatusInternalServerError, res.Err.Error())
		return
	}
	jsonOK(w, res.Info)
}
