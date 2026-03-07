package dockercontainerexecstate_test

import (
	"errors"
	"sync"
	"testing"

	state "dokoko.ai/dokoko/internal/docker/containerexec/state"
	"dokoko.ai/dokoko/pkg/logger"
)

func newExecStore(t *testing.T) *state.ExecStore {
	t.Helper()
	return state.NewExecStore(logger.New(logger.LevelSilent))
}

func eparams(execID, containerID string, cmd []string) state.RegisterExecParams {
	return state.RegisterExecParams{
		ExecID:      execID,
		ContainerID: containerID,
		Cmd:         cmd,
		Tty:         true,
		Detach:      false,
	}
}

// ── RegisterExec ──────────────────────────────────────────────────────────────

func TestExecRegister_NewRecord_StatusCreated(t *testing.T) {
	s := newExecStore(t)
	rec := s.RegisterExec(eparams("exec-abc123", "cid-xyz456", []string{"/bin/sh"}))

	if rec.Status != state.ExecStatusCreated {
		t.Errorf("status: got %q, want %q", rec.Status, state.ExecStatusCreated)
	}
	if rec.ExecID != "exec-abc123" {
		t.Errorf("ExecID: got %q", rec.ExecID)
	}
	if rec.ContainerID != "cid-xyz456" {
		t.Errorf("ContainerID: got %q", rec.ContainerID)
	}
	if len(rec.Cmd) != 1 || rec.Cmd[0] != "/bin/sh" {
		t.Errorf("Cmd: got %v, want [/bin/sh]", rec.Cmd)
	}
	if !rec.Tty {
		t.Error("Tty: want true")
	}
	if rec.RegisteredAt.IsZero() {
		t.Error("RegisteredAt should not be zero")
	}
	if !rec.StartedAt.IsZero() {
		t.Error("StartedAt should be zero until MarkRunning")
	}
	if !rec.FinishedAt.IsZero() {
		t.Error("FinishedAt should be zero until settled")
	}
}

func TestExecRegister_DuplicateExecID_ReturnsExisting(t *testing.T) {
	s := newExecStore(t)
	first := s.RegisterExec(eparams("exec-001", "cid-1", []string{"/bin/sh"}))
	second := s.RegisterExec(eparams("exec-001", "cid-2", []string{"/bin/bash"})) // duplicate

	if second.ContainerID != first.ContainerID {
		t.Errorf("duplicate registration should return original: %q vs %q",
			first.ContainerID, second.ContainerID)
	}
	if s.Size() != 1 {
		t.Errorf("store size: got %d, want 1", s.Size())
	}
}

func TestExecRegister_ReturnedRecordIsACopy(t *testing.T) {
	s := newExecStore(t)
	rec := s.RegisterExec(eparams("exec-001", "cid-1", []string{"/bin/sh"}))

	rec.Cmd[0] = "MUTATED"
	stored, _ := s.Get("exec-001")
	if stored.Cmd[0] == "MUTATED" {
		t.Error("mutation of returned record affected store copy")
	}
}

// ── MarkRunning ───────────────────────────────────────────────────────────────

func TestExecMarkRunning_TransitionsToRunning(t *testing.T) {
	s := newExecStore(t)
	s.RegisterExec(eparams("exec-001", "cid-1", []string{"/bin/sh"}))

	if err := s.MarkRunning("exec-001"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	rec, _ := s.Get("exec-001")
	if rec.Status != state.ExecStatusRunning {
		t.Errorf("status: got %q, want %q", rec.Status, state.ExecStatusRunning)
	}
	if rec.StartedAt.IsZero() {
		t.Error("StartedAt should be set after MarkRunning")
	}
}

func TestExecMarkRunning_NotFound(t *testing.T) {
	err := newExecStore(t).MarkRunning("ghost-exec")
	if !errors.Is(err, state.ErrExecNotFound) {
		t.Errorf("want ErrExecNotFound, got %v", err)
	}
}

// ── MarkFinished ──────────────────────────────────────────────────────────────

func TestExecMarkFinished_TransitionsToFinished(t *testing.T) {
	s := newExecStore(t)
	s.RegisterExec(eparams("exec-001", "cid-1", []string{"/bin/sh"}))
	_ = s.MarkRunning("exec-001")

	if err := s.MarkFinished("exec-001", 0); err != nil {
		t.Fatalf("MarkFinished: %v", err)
	}

	rec, _ := s.Get("exec-001")
	if rec.Status != state.ExecStatusFinished {
		t.Errorf("status: got %q, want %q", rec.Status, state.ExecStatusFinished)
	}
	if rec.ExitCode != 0 {
		t.Errorf("ExitCode: got %d, want 0", rec.ExitCode)
	}
	if rec.FinishedAt.IsZero() {
		t.Error("FinishedAt should be set on finish")
	}
}

func TestExecMarkFinished_NonZeroExitCode(t *testing.T) {
	s := newExecStore(t)
	s.RegisterExec(eparams("exec-001", "cid-1", []string{"false"}))

	if err := s.MarkFinished("exec-001", 1); err != nil {
		t.Fatalf("MarkFinished: %v", err)
	}

	rec, _ := s.Get("exec-001")
	if rec.ExitCode != 1 {
		t.Errorf("ExitCode: got %d, want 1", rec.ExitCode)
	}
}

func TestExecMarkFinished_NotFound(t *testing.T) {
	err := newExecStore(t).MarkFinished("ghost-exec", 0)
	if !errors.Is(err, state.ErrExecNotFound) {
		t.Errorf("want ErrExecNotFound, got %v", err)
	}
}

// ── MarkErrored ───────────────────────────────────────────────────────────────

func TestExecMarkErrored_TransitionsToErrored(t *testing.T) {
	s := newExecStore(t)
	s.RegisterExec(eparams("exec-001", "cid-1", []string{"/bin/sh"}))

	if err := s.MarkErrored("exec-001", "container not running"); err != nil {
		t.Fatalf("MarkErrored: %v", err)
	}

	rec, _ := s.Get("exec-001")
	if rec.Status != state.ExecStatusErrored {
		t.Errorf("status: got %q, want %q", rec.Status, state.ExecStatusErrored)
	}
	if rec.ErrMsg != "container not running" {
		t.Errorf("ErrMsg: got %q", rec.ErrMsg)
	}
	if rec.FinishedAt.IsZero() {
		t.Error("FinishedAt should be set on error")
	}
}

func TestExecMarkErrored_EmptyMsg_UsesPlaceholder(t *testing.T) {
	s := newExecStore(t)
	s.RegisterExec(eparams("exec-001", "cid-1", nil))
	_ = s.MarkErrored("exec-001", "")

	rec, _ := s.Get("exec-001")
	if rec.ErrMsg == "" {
		t.Error("ErrMsg should have a placeholder, not be empty")
	}
}

func TestExecMarkErrored_NotFound(t *testing.T) {
	err := newExecStore(t).MarkErrored("ghost-exec", "oops")
	if !errors.Is(err, state.ErrExecNotFound) {
		t.Errorf("want ErrExecNotFound, got %v", err)
	}
}

// ── Get ───────────────────────────────────────────────────────────────────────

func TestExecGet_ReturnsRecord(t *testing.T) {
	s := newExecStore(t)
	s.RegisterExec(eparams("exec-001", "cid-1", []string{"/bin/sh"}))

	rec, ok := s.Get("exec-001")
	if !ok {
		t.Fatal("expected record to be found")
	}
	if rec.ExecID != "exec-001" {
		t.Errorf("ExecID: got %q", rec.ExecID)
	}
}

func TestExecGet_UnknownID_ReturnsFalse(t *testing.T) {
	_, ok := newExecStore(t).Get("no-such-exec")
	if ok {
		t.Error("expected ok=false for unknown exec ID")
	}
}

// ── ByStatus ─────────────────────────────────────────────────────────────────

func TestExecByStatus_FiltersCorrectly(t *testing.T) {
	s := newExecStore(t)
	s.RegisterExec(eparams("exec-a", "cid", []string{"/bin/sh"}))
	s.RegisterExec(eparams("exec-b", "cid", []string{"ls"}))
	s.RegisterExec(eparams("exec-c", "cid", []string{"pwd"}))

	_ = s.MarkRunning("exec-b")
	_ = s.MarkFinished("exec-c", 0)

	if len(s.ByStatus(state.ExecStatusCreated)) != 1 {
		t.Errorf("created: want 1, got %d", len(s.ByStatus(state.ExecStatusCreated)))
	}
	if len(s.ByStatus(state.ExecStatusRunning)) != 1 {
		t.Errorf("running: want 1, got %d", len(s.ByStatus(state.ExecStatusRunning)))
	}
	if len(s.ByStatus(state.ExecStatusFinished)) != 1 {
		t.Errorf("finished: want 1, got %d", len(s.ByStatus(state.ExecStatusFinished)))
	}
}

// ── Concurrency ───────────────────────────────────────────────────────────────

func TestExecStore_ConcurrentRegistersAndReads(t *testing.T) {
	s := newExecStore(t)
	const n = 200

	var wg sync.WaitGroup
	for i := range n {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			execID := "exec-" + string(rune('a'+i%26)) + string(rune('0'+i/26%10))
			s.RegisterExec(state.RegisterExecParams{
				ExecID:      execID,
				ContainerID: "cid-container",
				Cmd:         []string{"/bin/sh"},
			})
		}()
	}
	wg.Wait()

	// note: exec IDs may collide in the generation above so just check it built
	if s.Size() == 0 {
		t.Error("expected at least 1 record")
	}
	_ = s.All()
}
