package server

import (
	"net/http"

	dockerimage "github.com/docker/docker/api/types/image"
)

// listImages returns all images from the in-memory store.
func (h *handler) listImages(w http.ResponseWriter, r *http.Request) {
	records := h.mgr.Images().Store().All()
	jsonOK(w, records)
}

// pullImage dispatches an async image pull.
// Body: {"ref":"ubuntu:22.04","platform":"linux/amd64"}
func (h *handler) pullImage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ref      string `json:"ref"`
		Platform string `json:"platform"`
	}
	if err := decode(r, &body); err != nil || body.Ref == "" {
		jsonErr(w, http.StatusBadRequest, "ref is required")
		return
	}
	ctx, cancel := opCtx(r)
	defer cancel()

	ticket, err := h.mgr.Images().Pull(ctx, body.Ref, dockerimage.PullOptions{Platform: body.Platform})
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := ticket.Wait(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonAccepted(w, "pull dispatched: "+body.Ref)
}

// removeImage dispatches an async image removal.
// Body: {"id":"sha256:...","force":true}
func (h *handler) removeImage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID    string `json:"id"`
		Force bool   `json:"force"`
	}
	if err := decode(r, &body); err != nil || body.ID == "" {
		jsonErr(w, http.StatusBadRequest, "id is required")
		return
	}
	ctx, cancel := opCtx(r)
	defer cancel()

	ticket, err := h.mgr.Images().Remove(ctx, body.ID, dockerimage.RemoveOptions{Force: body.Force})
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := ticket.Wait(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonAccepted(w, "remove dispatched: "+body.ID)
}

// tagImage tags source → target.
// Body: {"source":"ubuntu:22.04","target":"myubuntu:latest"}
func (h *handler) tagImage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Source string `json:"source"`
		Target string `json:"target"`
	}
	if err := decode(r, &body); err != nil || body.Source == "" || body.Target == "" {
		jsonErr(w, http.StatusBadRequest, "source and target are required")
		return
	}
	ctx, cancel := opCtx(r)
	defer cancel()

	ticket, err := h.mgr.Images().Tag(ctx, body.Source, body.Target)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := ticket.Wait(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonAccepted(w, "tagged "+body.Source+" → "+body.Target)
}

// refreshImages syncs the image store with live Docker data.
func (h *handler) refreshImages(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := opCtx(r)
	defer cancel()

	if err := h.mgr.Images().Refresh(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, "image store refreshed")
}

// inspectImage inspects a single image by ID or name.
func (h *handler) inspectImage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx, cancel := opCtx(r)
	defer cancel()

	res := <-h.mgr.Images().Inspect(ctx, id)
	if res.Err != nil {
		jsonErr(w, http.StatusInternalServerError, res.Err.Error())
		return
	}
	jsonOK(w, res.Info)
}
