package server

import (
	"net/http"
)

// health reports whether the server is up and Docker is reachable.
func (h *handler) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := opCtx(r)
	defer cancel()

	dockerOK := true
	errMsg := ""
	if err := h.mgr.Ping(ctx); err != nil {
		dockerOK = false
		errMsg = err.Error()
	}

	status := map[string]any{
		"ok":     dockerOK,
		"docker": dockerOK,
	}
	if errMsg != "" {
		status["error"] = errMsg
	}
	jsonOK(w, status)
}

// stateSummary holds counts from a single subsystem's state machine.
type stateSummary struct {
	Requested int `json:"requested"`
	Active    int `json:"active"`
	Failed    int `json:"failed"`
	Abandoned int `json:"abandoned"`
}

// getState returns operation state summaries for all six subsystems.
func (h *handler) getState(w http.ResponseWriter, r *http.Request) {
	summary := func(req, act, fail, aband int) stateSummary {
		return stateSummary{req, act, fail, aband}
	}

	req, act, fail, aband := h.mgr.ImageState().Summary()
	imgState := summary(req, act, fail, aband)

	req, act, fail, aband = h.mgr.ContainerState().Summary()
	ctrState := summary(req, act, fail, aband)

	req, act, fail, aband = h.mgr.VolumeState().Summary()
	volState := summary(req, act, fail, aband)

	req, act, fail, aband = h.mgr.NetworkState().Summary()
	netState := summary(req, act, fail, aband)

	req, act, fail, aband = h.mgr.BuildState().Summary()
	bldState := summary(req, act, fail, aband)

	req, act, fail, aband = h.mgr.ExecState().Summary()
	execState := summary(req, act, fail, aband)

	jsonOK(w, map[string]stateSummary{
		"images":     imgState,
		"containers": ctrState,
		"volumes":    volState,
		"networks":   netState,
		"builds":     bldState,
		"execs":      execState,
	})
}
