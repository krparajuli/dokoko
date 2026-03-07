package server

import (
	"net/http"
	"strings"

	dockertypes "github.com/docker/docker/api/types"
)

// createExec creates an exec instance in a running container.
// Body: {"container":"my-container","cmd":"/bin/sh -c 'echo hello'"}
func (h *handler) createExec(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Container string `json:"container"`
		Cmd       string `json:"cmd"`
	}
	if err := decode(r, &body); err != nil || body.Container == "" {
		jsonErr(w, http.StatusBadRequest, "container is required")
		return
	}
	ctx, cancel := opCtx(r)
	defer cancel()

	args := strings.Fields(body.Cmd)
	if len(args) == 0 {
		args = []string{"/bin/sh"}
	}

	cfg := dockertypes.ExecConfig{
		Cmd:          args,
		AttachStdout: true,
		AttachStderr: true,
	}
	ticket, err := h.mgr.Exec().Create(ctx, body.Container, cfg)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := ticket.Wait(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	execID, err := h.mgr.Exec().ExecDockerID(ticket.ChangeID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "exec created but ID unavailable: "+err.Error())
		return
	}
	jsonOK(w, map[string]string{"exec_id": execID, "container": body.Container})
}

// startExec starts an exec instance by ID.
// Body: {"detach":true}
func (h *handler) startExec(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Detach bool `json:"detach"`
	}
	// body is optional; ignore decode error
	_ = decode(r, &body)

	ctx, cancel := opCtx(r)
	defer cancel()

	ticket, err := h.mgr.Exec().Start(ctx, id, dockertypes.ExecStartCheck{Detach: body.Detach})
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := ticket.Wait(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonAccepted(w, "exec started: "+id)
}

// inspectExec inspects an exec instance by ID.
func (h *handler) inspectExec(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx, cancel := opCtx(r)
	defer cancel()

	res := <-h.mgr.Exec().Inspect(ctx, id)
	if res.Err != nil {
		jsonErr(w, http.StatusInternalServerError, res.Err.Error())
		return
	}
	jsonOK(w, res.Info)
}
