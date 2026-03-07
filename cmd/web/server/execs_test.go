package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	dockerexecactor "dokoko.ai/dokoko/internal/docker/containerexec/actor"
	dockertypes "github.com/docker/docker/api/types"
)

// ── Create exec ───────────────────────────────────────────────────────────────

func TestCreateExec_OK(t *testing.T) {
	var gotContainer string
	var gotCmd []string
	fake := defaultFake()
	fake.exec = &fakeExecActor{
		createFn: func(_ context.Context, containerID string, cfg dockertypes.ExecConfig) (*dockerexecactor.Ticket, error) {
			gotContainer = containerID
			gotCmd = cfg.Cmd
			return closedExecTicket("chg-create"), nil
		},
	}

	rec := httptest.NewRecorder()
	newTestHandler(fake).createExec(rec, newReq("POST", "/api/execs", jsonBody(map[string]string{
		"container": "my-container",
		"cmd":       "/bin/sh -c 'echo hello'",
	})))

	assertStatus(t, rec, http.StatusAccepted)
	if gotContainer != "my-container" {
		t.Errorf("container: got %q, want my-container", gotContainer)
	}
	if len(gotCmd) == 0 {
		t.Error("expected non-empty cmd slice")
	}
}

func TestCreateExec_DefaultsToShell(t *testing.T) {
	var gotCmd []string
	fake := defaultFake()
	fake.exec = &fakeExecActor{
		createFn: func(_ context.Context, _ string, cfg dockertypes.ExecConfig) (*dockerexecactor.Ticket, error) {
			gotCmd = cfg.Cmd
			return closedExecTicket("chg-create"), nil
		},
	}

	rec := httptest.NewRecorder()
	newTestHandler(fake).createExec(rec, newReq("POST", "/api/execs", jsonBody(map[string]string{
		"container": "my-container",
		"cmd":       "",
	})))

	assertStatus(t, rec, http.StatusAccepted)
	if len(gotCmd) == 0 || gotCmd[0] != "/bin/sh" {
		t.Errorf("expected default cmd [/bin/sh], got %v", gotCmd)
	}
}

func TestCreateExec_MissingContainer(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestHandler(defaultFake()).createExec(rec, newReq("POST", "/api/execs", jsonBody(map[string]string{"cmd": "/bin/sh"})))
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestCreateExec_DockerError(t *testing.T) {
	fake := defaultFake()
	fake.exec = &fakeExecActor{
		createFn: func(_ context.Context, _ string, _ dockertypes.ExecConfig) (*dockerexecactor.Ticket, error) {
			return nil, errors.New("container not running")
		},
	}

	rec := httptest.NewRecorder()
	newTestHandler(fake).createExec(rec, newReq("POST", "/api/execs", jsonBody(map[string]string{"container": "x"})))
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ── Start exec ────────────────────────────────────────────────────────────────

func TestStartExec_OK(t *testing.T) {
	var gotID string
	var gotDetach bool
	fake := defaultFake()
	fake.exec = &fakeExecActor{
		startFn: func(_ context.Context, execID string, cfg dockertypes.ExecStartCheck) (*dockerexecactor.Ticket, error) {
			gotID = execID
			gotDetach = cfg.Detach
			return closedExecTicket("chg-start"), nil
		},
	}

	r := newReq("POST", "/api/execs/exec123/start", jsonBody(map[string]bool{"detach": true}))
	r.SetPathValue("id", "exec123")
	rec := httptest.NewRecorder()
	newTestHandler(fake).startExec(rec, r)

	assertStatus(t, rec, http.StatusAccepted)
	if gotID != "exec123" {
		t.Errorf("Start called with %q, want exec123", gotID)
	}
	if !gotDetach {
		t.Error("expected Detach=true")
	}
}

func TestStartExec_Error(t *testing.T) {
	fake := defaultFake()
	fake.exec = &fakeExecActor{
		startFn: func(_ context.Context, _ string, _ dockertypes.ExecStartCheck) (*dockerexecactor.Ticket, error) {
			return nil, errors.New("exec not found")
		},
	}

	r := newReq("POST", "/api/execs/bad/start", nil)
	r.SetPathValue("id", "bad")
	rec := httptest.NewRecorder()
	newTestHandler(fake).startExec(rec, r)
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ── Inspect exec ──────────────────────────────────────────────────────────────

func TestInspectExec_OK(t *testing.T) {
	fake := defaultFake()
	fake.exec = &fakeExecActor{
		inspectFn: func(_ context.Context, execID string) <-chan dockerexecactor.InspectResult {
			ch := make(chan dockerexecactor.InspectResult, 1)
			ch <- dockerexecactor.InspectResult{
				Info: dockertypes.ContainerExecInspect{ExecID: execID, Running: false, ExitCode: 0},
			}
			return ch
		},
	}

	r := newReq("GET", "/api/execs/exec123/inspect", nil)
	r.SetPathValue("id", "exec123")
	rec := httptest.NewRecorder()
	newTestHandler(fake).inspectExec(rec, r)

	assertStatus(t, rec, http.StatusOK)
	resp := parseResp(t, rec)
	if !containsStr(string(resp.Data), "exec123") {
		t.Errorf("inspect response missing exec ID: %s", resp.Data)
	}
}

func TestInspectExec_Error(t *testing.T) {
	fake := defaultFake()
	fake.exec = &fakeExecActor{
		inspectFn: func(_ context.Context, _ string) <-chan dockerexecactor.InspectResult {
			ch := make(chan dockerexecactor.InspectResult, 1)
			ch <- dockerexecactor.InspectResult{Err: errors.New("not found")}
			return ch
		},
	}

	r := newReq("GET", "/api/execs/bad/inspect", nil)
	r.SetPathValue("id", "bad")
	rec := httptest.NewRecorder()
	newTestHandler(fake).inspectExec(rec, r)
	assertStatus(t, rec, http.StatusInternalServerError)
}
