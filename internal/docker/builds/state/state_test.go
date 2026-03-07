package dockerbuildstate_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	state "dokoko.ai/dokoko/internal/docker/builds/state"
	"dokoko.ai/dokoko/pkg/logger"
)

func newState(t *testing.T) *state.State {
	t.Helper()
	return state.New(logger.New(logger.LevelSilent))
}

// ── RequestChange ─────────────────────────────────────────────────────────────

func TestRequestChange_AppearsInRequested(t *testing.T) {
	s := newState(t)

	c := s.RequestChange(state.OpBuild, "myapp:1.0", nil)

	if c.ID == "" {
		t.Fatal("expected non-empty change ID")
	}
	if c.Op != state.OpBuild {
		t.Errorf("op: got %q, want %q", c.Op, state.OpBuild)
	}
	if c.Tags != "myapp:1.0" {
		t.Errorf("tags: got %q, want %q", c.Tags, "myapp:1.0")
	}
	if c.RequestedAt.IsZero() {
		t.Error("RequestedAt should not be zero")
	}

	req := s.Requested()
	if len(req) != 1 {
		t.Fatalf("requested: got %d items, want 1", len(req))
	}
	if req[0].ID != c.ID {
		t.Errorf("requested[0].ID=%q, want %q", req[0].ID, c.ID)
	}
}

func TestRequestChange_MetaIsCopied(t *testing.T) {
	s := newState(t)
	meta := map[string]string{"dockerfile": "Dockerfile"}

	c := s.RequestChange(state.OpBuild, "app:latest", meta)

	meta["dockerfile"] = "MUTATED"
	if c.Meta["dockerfile"] != "Dockerfile" {
		t.Errorf("meta was not copied: got %q, want %q", c.Meta["dockerfile"], "Dockerfile")
	}
}

func TestRequestChange_IDs_AreUnique(t *testing.T) {
	s := newState(t)
	const n = 500
	seen := make(map[string]struct{}, n)
	for i := range n {
		c := s.RequestChange(state.OpBuild, fmt.Sprintf("app:%d", i), nil)
		if _, dup := seen[c.ID]; dup {
			t.Fatalf("duplicate ID %q at i=%d", c.ID, i)
		}
		seen[c.ID] = struct{}{}
	}
}

func TestRequestChange_IDPrefix(t *testing.T) {
	s := newState(t)
	c := s.RequestChange(state.OpBuild, "app:latest", nil)
	if len(c.ID) < 5 || c.ID[:5] != "bchg-" {
		t.Errorf("expected ID to start with bchg-, got %q", c.ID)
	}
}

// ── ConfirmSuccess ────────────────────────────────────────────────────────────

func TestConfirmSuccess_MovesToActive(t *testing.T) {
	s := newState(t)
	c := s.RequestChange(state.OpBuild, "myapp:1.0", nil)

	rec, err := s.ConfirmSuccess(c.ID, "sha256:abc123")
	if err != nil {
		t.Fatalf("ConfirmSuccess: %v", err)
	}

	if len(s.Requested()) != 0 {
		t.Errorf("requested: want 0, got %d", len(s.Requested()))
	}
	active := s.Active()
	if len(active) != 1 {
		t.Fatalf("active: want 1, got %d", len(active))
	}
	if active[0].Change.ID != c.ID {
		t.Errorf("active[0].Change.ID=%q, want %q", active[0].Change.ID, c.ID)
	}
	if active[0].ImageID != "sha256:abc123" {
		t.Errorf("ImageID: got %q, want %q", active[0].ImageID, "sha256:abc123")
	}
	if active[0].ConfirmedAt.IsZero() {
		t.Error("ConfirmedAt should not be zero")
	}
	if rec.Change.ID != c.ID {
		t.Errorf("returned rec ID=%q, want %q", rec.Change.ID, c.ID)
	}
}

func TestConfirmSuccess_EmptyImageID_Allowed(t *testing.T) {
	// PruneCache legitimately returns no image ID.
	s := newState(t)
	c := s.RequestChange(state.OpPruneCache, "", nil)

	_, err := s.ConfirmSuccess(c.ID, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(s.Active()) != 1 {
		t.Fatalf("active: want 1, got %d", len(s.Active()))
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
	c := s.RequestChange(state.OpBuild, "app:1.0", nil)
	_, _ = s.ConfirmSuccess(c.ID, "sha256:abc")

	_, err := s.ConfirmSuccess(c.ID, "sha256:abc")
	if !errors.Is(err, state.ErrChangeNotFound) {
		t.Errorf("want ErrChangeNotFound on double-confirm, got %v", err)
	}
}

// ── RecordFailure ─────────────────────────────────────────────────────────────

func TestRecordFailure_MovesToFailed(t *testing.T) {
	s := newState(t)
	c := s.RequestChange(state.OpBuild, "bad:image", nil)
	opErr := errors.New("no such file: Dockerfile")

	rec, err := s.RecordFailure(c.ID, opErr)
	if err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}

	if len(s.Requested()) != 0 {
		t.Errorf("requested: want 0, got %d", len(s.Requested()))
	}
	failed := s.Failed()
	if len(failed) != 1 {
		t.Fatalf("failed: want 1, got %d", len(failed))
	}
	if failed[0].Err != opErr.Error() {
		t.Errorf("Err: got %q, want %q", failed[0].Err, opErr.Error())
	}
	if failed[0].FailedAt.IsZero() {
		t.Error("FailedAt should not be zero")
	}
	if rec.Change.ID != c.ID {
		t.Errorf("returned rec ID=%q, want %q", rec.Change.ID, c.ID)
	}
}

func TestRecordFailure_NilError_UsesPlaceholder(t *testing.T) {
	s := newState(t)
	c := s.RequestChange(state.OpBuild, "app:latest", nil)

	rec, err := s.RecordFailure(c.ID, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Err == "" {
		t.Error("Err should have a placeholder, not be empty")
	}
}

func TestRecordFailure_NotFound(t *testing.T) {
	s := newState(t)

	_, err := s.RecordFailure("ghost-id", errors.New("oops"))
	if !errors.Is(err, state.ErrChangeNotFound) {
		t.Errorf("want ErrChangeNotFound, got %v", err)
	}
}

// ── Abandon ───────────────────────────────────────────────────────────────────

func TestAbandon_MovesToAbandoned(t *testing.T) {
	s := newState(t)
	c := s.RequestChange(state.OpBuild, "app:latest", nil)

	rec, err := s.Abandon(c.ID, "context cancelled")
	if err != nil {
		t.Fatalf("Abandon: %v", err)
	}

	if len(s.Requested()) != 0 {
		t.Errorf("requested: want 0, got %d", len(s.Requested()))
	}
	abn := s.Abandoned()
	if len(abn) != 1 {
		t.Fatalf("abandoned: want 1, got %d", len(abn))
	}
	if abn[0].Reason != "context cancelled" {
		t.Errorf("Reason: got %q", abn[0].Reason)
	}
	if abn[0].AbandonedAt.IsZero() {
		t.Error("AbandonedAt should not be zero")
	}
	if rec.Change.ID != c.ID {
		t.Errorf("returned rec ID=%q, want %q", rec.Change.ID, c.ID)
	}
}

func TestAbandon_EmptyReason_UsesPlaceholder(t *testing.T) {
	s := newState(t)
	c := s.RequestChange(state.OpBuild, "app:1.0", nil)

	rec, err := s.Abandon(c.ID, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Reason == "" {
		t.Error("Reason should have a placeholder, not be empty")
	}
}

func TestAbandon_NotFound(t *testing.T) {
	s := newState(t)

	_, err := s.Abandon("ghost-id", "test")
	if !errors.Is(err, state.ErrChangeNotFound) {
		t.Errorf("want ErrChangeNotFound, got %v", err)
	}
}

// ── Summary ───────────────────────────────────────────────────────────────────

func TestSummary_ReflectsAllBuckets(t *testing.T) {
	s := newState(t)

	a := s.RequestChange(state.OpBuild, "app:1", nil)
	b := s.RequestChange(state.OpBuild, "app:2", nil)
	c := s.RequestChange(state.OpPruneCache, "", nil)
	s.RequestChange(state.OpBuild, "app:3", nil) // left in requested

	_, _ = s.ConfirmSuccess(a.ID, "sha256:aaa")
	_, _ = s.RecordFailure(b.ID, errors.New("bad"))
	_, _ = s.Abandon(c.ID, "cancelled")

	req, act, fail, abn := s.Summary()
	if req != 1 {
		t.Errorf("requested: got %d, want 1", req)
	}
	if act != 1 {
		t.Errorf("active: got %d, want 1", act)
	}
	if fail != 1 {
		t.Errorf("failed: got %d, want 1", fail)
	}
	if abn != 1 {
		t.Errorf("abandoned: got %d, want 1", abn)
	}
}

// ── FindByID ──────────────────────────────────────────────────────────────────

func TestFindByID_Requested(t *testing.T) {
	s := newState(t)
	c := s.RequestChange(state.OpBuild, "app:1.0", nil)

	status, rec, err := s.FindByID(c.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if status != state.StatusRequested {
		t.Errorf("status: got %q, want %q", status, state.StatusRequested)
	}
	if rec.(*state.StateChange).ID != c.ID {
		t.Errorf("record ID mismatch")
	}
}

func TestFindByID_Active(t *testing.T) {
	s := newState(t)
	c := s.RequestChange(state.OpBuild, "app:1.0", nil)
	_, _ = s.ConfirmSuccess(c.ID, "sha256:abc")

	status, _, err := s.FindByID(c.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if status != state.StatusActive {
		t.Errorf("status: got %q, want %q", status, state.StatusActive)
	}
}

func TestFindByID_Failed(t *testing.T) {
	s := newState(t)
	c := s.RequestChange(state.OpBuild, "app:1.0", nil)
	_, _ = s.RecordFailure(c.ID, errors.New("oops"))

	status, _, err := s.FindByID(c.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if status != state.StatusFailed {
		t.Errorf("status: got %q, want %q", status, state.StatusFailed)
	}
}

func TestFindByID_Abandoned(t *testing.T) {
	s := newState(t)
	c := s.RequestChange(state.OpBuild, "app:1.0", nil)
	_, _ = s.Abandon(c.ID, "test")

	status, _, err := s.FindByID(c.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if status != state.StatusAbandoned {
		t.Errorf("status: got %q, want %q", status, state.StatusAbandoned)
	}
}

func TestFindByID_NotFound(t *testing.T) {
	s := newState(t)

	_, _, err := s.FindByID("no-such-id")
	if !errors.Is(err, state.ErrChangeNotFound) {
		t.Errorf("want ErrChangeNotFound, got %v", err)
	}
}

// ── Snapshot isolation ────────────────────────────────────────────────────────

func TestSnapshots_DoNotAliasInternalSlice(t *testing.T) {
	s := newState(t)
	s.RequestChange(state.OpBuild, "app:latest", nil)

	snap := s.Requested()
	snap[0] = nil

	if s.Requested()[0] == nil {
		t.Error("mutating snapshot affected internal requested slice")
	}
}

// ── Concurrency ───────────────────────────────────────────────────────────────

func TestConcurrentRequestsAndResolutions(t *testing.T) {
	s := newState(t)
	const workers = 20
	const changesPerWorker = 50

	var wg sync.WaitGroup
	ids := make(chan string, workers*changesPerWorker)

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range changesPerWorker {
				c := s.RequestChange(state.OpBuild, fmt.Sprintf("app:%d", i), nil)
				ids <- c.ID
			}
		}()
	}
	wg.Wait()
	close(ids)

	total := workers * changesPerWorker
	if n := len(s.Requested()); n != total {
		t.Fatalf("requested: got %d, want %d", n, total)
	}

	allIDs := make([]string, 0, total)
	for id := range ids {
		allIDs = append(allIDs, id)
	}

	for i, id := range allIDs {
		i, id := i, id
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				_, _ = s.ConfirmSuccess(id, "sha256:abc")
			} else {
				_, _ = s.RecordFailure(id, errors.New("test"))
			}
		}()
	}
	wg.Wait()

	req, act, fail, abn := s.Summary()
	if req != 0 {
		t.Errorf("requested after resolution: got %d, want 0", req)
	}
	if act+fail != total {
		t.Errorf("active(%d)+failed(%d) should equal %d", act, fail, total)
	}
	if abn != 0 {
		t.Errorf("abandoned: got %d, want 0", abn)
	}
}
