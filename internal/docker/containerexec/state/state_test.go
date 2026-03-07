package dockercontainerexecstate_test

import (
	"errors"
	"sync"
	"testing"

	state "dokoko.ai/dokoko/internal/docker/containerexec/state"
	"dokoko.ai/dokoko/pkg/logger"
)

func newState(t *testing.T) *state.State {
	t.Helper()
	return state.New(logger.New(logger.LevelSilent))
}

// ── RequestChange ─────────────────────────────────────────────────────────────

func TestRequestChange_AppearsInRequested(t *testing.T) {
	s := newState(t)
	c := s.RequestChange(state.OpExecCreate, "container-abc", map[string]string{"cmd": "ls"})

	if c.ID == "" {
		t.Error("expected non-empty change ID")
	}
	if c.Op != state.OpExecCreate {
		t.Errorf("want op=%s got %s", state.OpExecCreate, c.Op)
	}
	if c.ExecRef != "container-abc" {
		t.Errorf("want ExecRef=%q got %q", "container-abc", c.ExecRef)
	}

	all := s.Requested()
	if len(all) != 1 || all[0].ID != c.ID {
		t.Errorf("expected 1 requested change with id=%s, got %v", c.ID, all)
	}
}

func TestRequestChange_MetaIsCopied(t *testing.T) {
	s := newState(t)
	meta := map[string]string{"cmd": "echo hi"}
	c := s.RequestChange(state.OpExecCreate, "ctr", meta)

	meta["cmd"] = "mutated"
	if c.Meta["cmd"] != "echo hi" {
		t.Errorf("meta was not copied: got %q", c.Meta["cmd"])
	}
}

func TestRequestChange_IDs_AreUnique(t *testing.T) {
	s := newState(t)
	seen := make(map[string]bool)
	for range 50 {
		c := s.RequestChange(state.OpExecCreate, "ctr", nil)
		if seen[c.ID] {
			t.Fatalf("duplicate change ID: %s", c.ID)
		}
		seen[c.ID] = true
	}
}

// ── ConfirmSuccess ────────────────────────────────────────────────────────────

func TestConfirmSuccess_MovesToActive(t *testing.T) {
	s := newState(t)
	c := s.RequestChange(state.OpExecCreate, "ctr", nil)

	rec, err := s.ConfirmSuccess(c.ID, "exec-id-abc123")
	if err != nil {
		t.Fatalf("ConfirmSuccess: %v", err)
	}
	if rec.ExecID != "exec-id-abc123" {
		t.Errorf("want ExecID=%q got %q", "exec-id-abc123", rec.ExecID)
	}
	if len(s.Requested()) != 0 {
		t.Errorf("expected 0 requested, got %d", len(s.Requested()))
	}
	if len(s.Active()) != 1 {
		t.Errorf("expected 1 active, got %d", len(s.Active()))
	}
}

func TestConfirmSuccess_EmptyExecID_Allowed(t *testing.T) {
	s := newState(t)
	c := s.RequestChange(state.OpExecStart, "exec-id-xyz", nil)

	rec, err := s.ConfirmSuccess(c.ID, "")
	if err != nil {
		t.Fatalf("ConfirmSuccess with empty execID: %v", err)
	}
	if rec.ExecID != "" {
		t.Errorf("expected empty ExecID for start op, got %q", rec.ExecID)
	}
}

func TestConfirmSuccess_NotFound(t *testing.T) {
	s := newState(t)
	_, err := s.ConfirmSuccess("nonexistent-id", "")
	if !errors.Is(err, state.ErrChangeNotFound) {
		t.Errorf("want ErrChangeNotFound, got %v", err)
	}
}

func TestConfirmSuccess_AlreadyConfirmed_NotFound(t *testing.T) {
	s := newState(t)
	c := s.RequestChange(state.OpExecCreate, "ctr", nil)
	_, _ = s.ConfirmSuccess(c.ID, "exec-123")
	_, err := s.ConfirmSuccess(c.ID, "exec-123")
	if !errors.Is(err, state.ErrChangeNotFound) {
		t.Errorf("want ErrChangeNotFound on double confirm, got %v", err)
	}
}

// ── RecordFailure ─────────────────────────────────────────────────────────────

func TestRecordFailure_MovesToFailed(t *testing.T) {
	s := newState(t)
	c := s.RequestChange(state.OpExecCreate, "ctr", nil)

	rec, err := s.RecordFailure(c.ID, errors.New("container not found"))
	if err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	if rec.Err == "" {
		t.Error("expected non-empty Err in FailedRecord")
	}
	if len(s.Requested()) != 0 {
		t.Errorf("expected 0 requested, got %d", len(s.Requested()))
	}
	if len(s.Failed()) != 1 {
		t.Errorf("expected 1 failed, got %d", len(s.Failed()))
	}
}

func TestRecordFailure_NilError_UsesPlaceholder(t *testing.T) {
	s := newState(t)
	c := s.RequestChange(state.OpExecCreate, "ctr", nil)

	rec, err := s.RecordFailure(c.ID, nil)
	if err != nil {
		t.Fatalf("RecordFailure(nil): %v", err)
	}
	if rec.Err == "" {
		t.Error("expected placeholder error text, got empty string")
	}
}

func TestRecordFailure_NotFound(t *testing.T) {
	s := newState(t)
	_, err := s.RecordFailure("missing", errors.New("oops"))
	if !errors.Is(err, state.ErrChangeNotFound) {
		t.Errorf("want ErrChangeNotFound, got %v", err)
	}
}

// ── Abandon ───────────────────────────────────────────────────────────────────

func TestAbandon_MovesToAbandoned(t *testing.T) {
	s := newState(t)
	c := s.RequestChange(state.OpExecStart, "exec-xyz", nil)

	rec, err := s.Abandon(c.ID, "context cancelled")
	if err != nil {
		t.Fatalf("Abandon: %v", err)
	}
	if rec.Reason != "context cancelled" {
		t.Errorf("want reason=%q got %q", "context cancelled", rec.Reason)
	}
	if len(s.Requested()) != 0 {
		t.Errorf("expected 0 requested, got %d", len(s.Requested()))
	}
	if len(s.Abandoned()) != 1 {
		t.Errorf("expected 1 abandoned, got %d", len(s.Abandoned()))
	}
}

func TestAbandon_EmptyReason_UsesPlaceholder(t *testing.T) {
	s := newState(t)
	c := s.RequestChange(state.OpExecResize, "exec-xyz", nil)

	rec, err := s.Abandon(c.ID, "")
	if err != nil {
		t.Fatalf("Abandon(empty reason): %v", err)
	}
	if rec.Reason == "" {
		t.Error("expected placeholder reason, got empty string")
	}
}

func TestAbandon_NotFound(t *testing.T) {
	s := newState(t)
	_, err := s.Abandon("ghost", "ctx")
	if !errors.Is(err, state.ErrChangeNotFound) {
		t.Errorf("want ErrChangeNotFound, got %v", err)
	}
}

// ── Summary ───────────────────────────────────────────────────────────────────

func TestSummary_ReflectsAllBuckets(t *testing.T) {
	s := newState(t)

	c1 := s.RequestChange(state.OpExecCreate, "ctr", nil)
	c2 := s.RequestChange(state.OpExecStart, "exec1", nil)
	c3 := s.RequestChange(state.OpExecResize, "exec2", nil)
	c4 := s.RequestChange(state.OpExecCreate, "ctr2", nil)

	_, _ = s.ConfirmSuccess(c1.ID, "exec-new")
	_, _ = s.RecordFailure(c2.ID, errors.New("oops"))
	_, _ = s.Abandon(c3.ID, "queue full")
	// c4 stays requested

	req, act, fail, abn := s.Summary()
	if req != 1 || act != 1 || fail != 1 || abn != 1 {
		t.Errorf("want (1,1,1,1) got (%d,%d,%d,%d)", req, act, fail, abn)
	}
	_ = c4
}

// ── FindByID ──────────────────────────────────────────────────────────────────

func TestFindByID_Requested(t *testing.T) {
	s := newState(t)
	c := s.RequestChange(state.OpExecCreate, "ctr", nil)

	status, rec, err := s.FindByID(c.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if status != state.StatusRequested {
		t.Errorf("want StatusRequested, got %s", status)
	}
	if rec.(*state.StateChange).ID != c.ID {
		t.Error("record ID mismatch")
	}
}

func TestFindByID_Active(t *testing.T) {
	s := newState(t)
	c := s.RequestChange(state.OpExecCreate, "ctr", nil)
	_, _ = s.ConfirmSuccess(c.ID, "exec-abc")

	status, _, err := s.FindByID(c.ID)
	if err != nil || status != state.StatusActive {
		t.Errorf("want StatusActive, got status=%s err=%v", status, err)
	}
}

func TestFindByID_Failed(t *testing.T) {
	s := newState(t)
	c := s.RequestChange(state.OpExecCreate, "ctr", nil)
	_, _ = s.RecordFailure(c.ID, errors.New("boom"))

	status, _, err := s.FindByID(c.ID)
	if err != nil || status != state.StatusFailed {
		t.Errorf("want StatusFailed, got status=%s err=%v", status, err)
	}
}

func TestFindByID_Abandoned(t *testing.T) {
	s := newState(t)
	c := s.RequestChange(state.OpExecResize, "exec-xyz", nil)
	_, _ = s.Abandon(c.ID, "actor closed")

	status, _, err := s.FindByID(c.ID)
	if err != nil || status != state.StatusAbandoned {
		t.Errorf("want StatusAbandoned, got status=%s err=%v", status, err)
	}
}

func TestFindByID_NotFound(t *testing.T) {
	s := newState(t)
	_, _, err := s.FindByID("does-not-exist")
	if !errors.Is(err, state.ErrChangeNotFound) {
		t.Errorf("want ErrChangeNotFound, got %v", err)
	}
}

// ── Snapshot isolation ────────────────────────────────────────────────────────

func TestSnapshots_DoNotAliasInternalSlice(t *testing.T) {
	s := newState(t)
	c := s.RequestChange(state.OpExecCreate, "ctr", nil)

	snap := s.Requested()
	snap[0] = nil // mutate snapshot

	all := s.Requested()
	if len(all) != 1 || all[0].ID != c.ID {
		t.Error("snapshot mutation affected internal state")
	}
}

// ── Concurrent access ─────────────────────────────────────────────────────────

func TestConcurrentRequestsAndResolutions(t *testing.T) {
	s := newState(t)
	const n = 100

	var wg sync.WaitGroup
	changes := make(chan *state.StateChange, n)

	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := s.RequestChange(state.OpExecCreate, "ctr", nil)
			changes <- c
		}()
	}
	wg.Wait()
	close(changes)

	for c := range changes {
		wg.Add(1)
		go func(c *state.StateChange) {
			defer wg.Done()
			_, _ = s.ConfirmSuccess(c.ID, "exec-id")
		}(c)
	}
	wg.Wait()

	req, act, _, _ := s.Summary()
	if req != 0 || act != n {
		t.Errorf("want (0, %d) got (%d, %d)", n, req, act)
	}
}
