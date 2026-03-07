package dockerbuildstate_test

import (
	"errors"
	"sync"
	"testing"

	state "dokoko.ai/dokoko/internal/docker/builds/state"
	"dokoko.ai/dokoko/pkg/logger"
)

func newBuildStore(t *testing.T) *state.BuildStore {
	t.Helper()
	return state.NewBuildStore(logger.New(logger.LevelSilent))
}

func bparams(changeID string, tags []string) state.RegisterBuildParams {
	return state.RegisterBuildParams{
		ChangeID:   changeID,
		Tags:       tags,
		Dockerfile: "Dockerfile",
		ContextDir: ".",
		Platform:   "linux/amd64",
	}
}

// ── RegisterBuild ─────────────────────────────────────────────────────────────

func TestBuildRegister_NewRecord_StatusPending(t *testing.T) {
	s := newBuildStore(t)
	rec := s.RegisterBuild(bparams("chg-001", []string{"myapp:latest"}))

	if rec.Status != state.BuildStatusPending {
		t.Errorf("status: got %q, want %q", rec.Status, state.BuildStatusPending)
	}
	if rec.ChangeID != "chg-001" {
		t.Errorf("ChangeID: got %q", rec.ChangeID)
	}
	if len(rec.Tags) != 1 || rec.Tags[0] != "myapp:latest" {
		t.Errorf("Tags: got %v, want [myapp:latest]", rec.Tags)
	}
	if rec.RegisteredAt.IsZero() {
		t.Error("RegisteredAt should not be zero")
	}
	if !rec.FinishedAt.IsZero() {
		t.Error("FinishedAt should be zero for pending builds")
	}
}

func TestBuildRegister_DuplicateChangeID_ReturnsExisting(t *testing.T) {
	s := newBuildStore(t)
	first := s.RegisterBuild(bparams("chg-001", []string{"img:v1"}))
	second := s.RegisterBuild(bparams("chg-001", []string{"img:v2"})) // duplicate

	if second.Tags[0] != first.Tags[0] {
		t.Errorf("duplicate registration should return original record, tags differ: %v vs %v",
			first.Tags, second.Tags)
	}
	if s.Size() != 1 {
		t.Errorf("store size: got %d, want 1", s.Size())
	}
}

func TestBuildRegister_ReturnedRecordIsACopy(t *testing.T) {
	s := newBuildStore(t)
	rec := s.RegisterBuild(bparams("chg-001", []string{"img:v1"}))

	rec.Tags[0] = "MUTATED"
	stored, _ := s.Get("chg-001")
	if stored.Tags[0] == "MUTATED" {
		t.Error("mutation of returned record affected store copy")
	}
}

// ── MarkSucceeded ─────────────────────────────────────────────────────────────

func TestBuildMarkSucceeded_TransitionsToSucceeded(t *testing.T) {
	s := newBuildStore(t)
	s.RegisterBuild(bparams("chg-001", []string{"img:v1"}))

	if err := s.MarkSucceeded("chg-001", "sha256:deadbeef1234"); err != nil {
		t.Fatalf("MarkSucceeded: %v", err)
	}

	rec, _ := s.Get("chg-001")
	if rec.Status != state.BuildStatusSucceeded {
		t.Errorf("status: got %q, want %q", rec.Status, state.BuildStatusSucceeded)
	}
	if rec.ResultImageID != "sha256:deadbeef1234" {
		t.Errorf("ResultImageID: got %q", rec.ResultImageID)
	}
	if rec.FinishedAt.IsZero() {
		t.Error("FinishedAt should be set on success")
	}
}

func TestBuildMarkSucceeded_NotFound(t *testing.T) {
	err := newBuildStore(t).MarkSucceeded("ghost", "sha256:abc")
	if !errors.Is(err, state.ErrBuildNotFound) {
		t.Errorf("want ErrBuildNotFound, got %v", err)
	}
}

// ── MarkFailed ────────────────────────────────────────────────────────────────

func TestBuildMarkFailed_TransitionsToFailed(t *testing.T) {
	s := newBuildStore(t)
	s.RegisterBuild(bparams("chg-002", []string{"img:v2"}))

	if err := s.MarkFailed("chg-002", "context deadline exceeded"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	rec, _ := s.Get("chg-002")
	if rec.Status != state.BuildStatusFailed {
		t.Errorf("status: got %q, want %q", rec.Status, state.BuildStatusFailed)
	}
	if rec.ErrMsg != "context deadline exceeded" {
		t.Errorf("ErrMsg: got %q", rec.ErrMsg)
	}
	if rec.FinishedAt.IsZero() {
		t.Error("FinishedAt should be set on failure")
	}
}

func TestBuildMarkFailed_EmptyMsg_UsesPlaceholder(t *testing.T) {
	s := newBuildStore(t)
	s.RegisterBuild(bparams("chg-002", nil))
	_ = s.MarkFailed("chg-002", "")

	rec, _ := s.Get("chg-002")
	if rec.ErrMsg == "" {
		t.Error("ErrMsg should have a placeholder, not be empty")
	}
}

func TestBuildMarkFailed_NotFound(t *testing.T) {
	err := newBuildStore(t).MarkFailed("ghost", "error")
	if !errors.Is(err, state.ErrBuildNotFound) {
		t.Errorf("want ErrBuildNotFound, got %v", err)
	}
}

// ── MarkAbandoned ─────────────────────────────────────────────────────────────

func TestBuildMarkAbandoned_TransitionsToAbandoned(t *testing.T) {
	s := newBuildStore(t)
	s.RegisterBuild(bparams("chg-003", []string{"img:v3"}))

	if err := s.MarkAbandoned("chg-003", "context cancelled"); err != nil {
		t.Fatalf("MarkAbandoned: %v", err)
	}

	rec, _ := s.Get("chg-003")
	if rec.Status != state.BuildStatusAbandoned {
		t.Errorf("status: got %q, want %q", rec.Status, state.BuildStatusAbandoned)
	}
	if rec.ErrMsg != "context cancelled" {
		t.Errorf("ErrMsg: got %q", rec.ErrMsg)
	}
	if rec.FinishedAt.IsZero() {
		t.Error("FinishedAt should be set on abandon")
	}
}

func TestBuildMarkAbandoned_EmptyReason_UsesPlaceholder(t *testing.T) {
	s := newBuildStore(t)
	s.RegisterBuild(bparams("chg-003", nil))
	_ = s.MarkAbandoned("chg-003", "")

	rec, _ := s.Get("chg-003")
	if rec.ErrMsg == "" {
		t.Error("ErrMsg/reason should have a placeholder, not be empty")
	}
}

func TestBuildMarkAbandoned_NotFound(t *testing.T) {
	err := newBuildStore(t).MarkAbandoned("ghost", "cancelled")
	if !errors.Is(err, state.ErrBuildNotFound) {
		t.Errorf("want ErrBuildNotFound, got %v", err)
	}
}

// ── Get ───────────────────────────────────────────────────────────────────────

func TestBuildGet_ReturnsRecord(t *testing.T) {
	s := newBuildStore(t)
	s.RegisterBuild(bparams("chg-001", []string{"img:v1"}))

	rec, ok := s.Get("chg-001")
	if !ok {
		t.Fatal("expected record to be found")
	}
	if rec.ChangeID != "chg-001" {
		t.Errorf("ChangeID: got %q", rec.ChangeID)
	}
}

func TestBuildGet_UnknownID_ReturnsFalse(t *testing.T) {
	_, ok := newBuildStore(t).Get("no-such-id")
	if ok {
		t.Error("expected ok=false for unknown ID")
	}
}

// ── ByStatus ─────────────────────────────────────────────────────────────────

func TestBuildByStatus_FiltersCorrectly(t *testing.T) {
	s := newBuildStore(t)
	s.RegisterBuild(bparams("chg-a", []string{"img:a"}))
	s.RegisterBuild(bparams("chg-b", []string{"img:b"}))
	s.RegisterBuild(bparams("chg-c", []string{"img:c"}))

	_ = s.MarkSucceeded("chg-a", "sha256:aaa")
	_ = s.MarkFailed("chg-b", "oops")

	if len(s.ByStatus(state.BuildStatusPending)) != 1 {
		t.Errorf("pending: want 1, got %d", len(s.ByStatus(state.BuildStatusPending)))
	}
	if len(s.ByStatus(state.BuildStatusSucceeded)) != 1 {
		t.Errorf("succeeded: want 1, got %d", len(s.ByStatus(state.BuildStatusSucceeded)))
	}
	if len(s.ByStatus(state.BuildStatusFailed)) != 1 {
		t.Errorf("failed: want 1, got %d", len(s.ByStatus(state.BuildStatusFailed)))
	}
}

// ── Concurrency ───────────────────────────────────────────────────────────────

func TestBuildStore_ConcurrentRegistersAndReads(t *testing.T) {
	s := newBuildStore(t)
	const n = 200

	var wg sync.WaitGroup
	for i := range n {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.RegisterBuild(state.RegisterBuildParams{
				ChangeID: func() string {
					return "chg-" + string(rune('a'+i%26)) + "-" + string(rune('0'+i/26))
				}(),
			})
		}()
	}
	wg.Wait()

	if s.Size() != n {
		t.Errorf("store size: got %d, want %d", s.Size(), n)
	}
}
