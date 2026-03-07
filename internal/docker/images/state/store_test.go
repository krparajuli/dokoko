package dockerimagestate_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	state "dokoko.ai/dokoko/internal/docker/images/state"
	"dokoko.ai/dokoko/pkg/logger"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func newStore(t *testing.T) *state.Store {
	t.Helper()
	return state.NewStore(logger.New(logger.LevelSilent))
}

func params(dockerID string, tags []string, origin state.Origin) state.RegisterParams {
	return state.RegisterParams{
		DockerID:       dockerID,
		RepoTags:       tags,
		OS:             "linux",
		Architecture:   "amd64",
		Size:           1024,
		ImageCreatedAt: time.Now(),
		Layers:         []string{"sha256:layer1", "sha256:layer2"},
		Origin:         origin,
	}
}

// ── Register ─────────────────────────────────────────────────────────────────

func TestRegister_NewRecord_StatusPresent(t *testing.T) {
	s := newStore(t)
	rec := s.Register(params("sha256:abc123", []string{"ubuntu:22.04"}, state.OriginInBand))

	if rec.Status != state.ImageStatusPresent {
		t.Errorf("status: got %q, want %q", rec.Status, state.ImageStatusPresent)
	}
	if rec.DockerID != "sha256:abc123" {
		t.Errorf("DockerID: got %q", rec.DockerID)
	}
	if rec.ShortID != "abc123" {
		t.Errorf("ShortID: got %q, want %q", rec.ShortID, "abc123")
	}
	if rec.Origin != state.OriginInBand {
		t.Errorf("Origin: got %q, want %q", rec.Origin, state.OriginInBand)
	}
	if rec.RegisteredAt.IsZero() {
		t.Error("RegisteredAt should not be zero")
	}
}

func TestRegister_SetsFingerprint(t *testing.T) {
	s := newStore(t)
	p := state.RegisterParams{
		DockerID: "sha256:deadbeef1234",
		Layers:   []string{"sha256:layerA", "sha256:layerB"},
		Origin:   state.OriginInBand,
	}
	rec := s.Register(p)

	if rec.Fingerprint.ConfigDigest != "deadbeef1234" {
		t.Errorf("ConfigDigest: got %q, want %q", rec.Fingerprint.ConfigDigest, "deadbeef1234")
	}
	if rec.Fingerprint.LayerChain != "sha256:layerA:sha256:layerB" {
		t.Errorf("LayerChain: got %q", rec.Fingerprint.LayerChain)
	}
}

func TestRegister_DockerIDWithoutPrefix_ShortID(t *testing.T) {
	s := newStore(t)
	rec := s.Register(params("abcdef123456789", nil, state.OriginOutOfBand))
	if rec.ShortID != "abcdef123456" {
		t.Errorf("ShortID: got %q, want %q", rec.ShortID, "abcdef123456")
	}
}

func TestRegister_Upsert_UpdatesMutableFields(t *testing.T) {
	s := newStore(t)
	s.Register(params("sha256:abc", []string{"img:v1"}, state.OriginInBand))

	// Re-register with updated tags.
	updated := s.Register(params("sha256:abc", []string{"img:v2", "img:latest"}, state.OriginInBand))

	if len(updated.RepoTags) != 2 {
		t.Fatalf("RepoTags: got %v, want 2 entries", updated.RepoTags)
	}
	if s.Size() != 1 {
		t.Errorf("store size: got %d, want 1 (upsert not insert)", s.Size())
	}
}

func TestRegister_Upsert_PreservesRegisteredAt(t *testing.T) {
	s := newStore(t)
	first := s.Register(params("sha256:abc", nil, state.OriginInBand))
	time.Sleep(2 * time.Millisecond)
	second := s.Register(params("sha256:abc", []string{"img:new"}, state.OriginInBand))

	if !second.RegisteredAt.Equal(first.RegisteredAt) {
		t.Errorf("RegisteredAt changed on upsert: first=%v second=%v",
			first.RegisteredAt, second.RegisteredAt)
	}
}

func TestRegister_ResetsStatusToPresent(t *testing.T) {
	s := newStore(t)
	s.Register(params("sha256:abc", nil, state.OriginInBand))
	_ = s.MarkDeleted("sha256:abc")

	rec := s.Register(params("sha256:abc", nil, state.OriginInBand))
	if rec.Status != state.ImageStatusPresent {
		t.Errorf("re-register should restore to Present, got %q", rec.Status)
	}
}

func TestRegister_UpgradesOriginOutOfBandToInBand(t *testing.T) {
	s := newStore(t)
	s.Register(params("sha256:abc", nil, state.OriginOutOfBand))

	updated := s.Register(params("sha256:abc", nil, state.OriginInBand))
	if updated.Origin != state.OriginInBand {
		t.Errorf("Origin: want InBand after upgrade, got %q", updated.Origin)
	}
}

func TestRegister_DoesNotDowngradeOriginInBandToOutOfBand(t *testing.T) {
	s := newStore(t)
	s.Register(params("sha256:abc", nil, state.OriginInBand))

	updated := s.Register(params("sha256:abc", nil, state.OriginOutOfBand))
	if updated.Origin != state.OriginInBand {
		t.Errorf("Origin: InBand should not be downgraded to OutOfBand, got %q", updated.Origin)
	}
}

func TestRegister_ReturnedRecordIsACopy(t *testing.T) {
	s := newStore(t)
	rec := s.Register(params("sha256:abc", []string{"tag:1"}, state.OriginInBand))

	// Mutating the returned record must not affect the store.
	rec.RepoTags[0] = "MUTATED"
	stored, _ := s.Get("sha256:abc")
	if stored.RepoTags[0] == "MUTATED" {
		t.Error("mutation of returned record affected store copy")
	}
}

// ── MarkDeleted ───────────────────────────────────────────────────────────────

func TestMarkDeleted_TransitionsToDeleted(t *testing.T) {
	s := newStore(t)
	s.Register(params("sha256:abc", nil, state.OriginInBand))

	if err := s.MarkDeleted("sha256:abc"); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}

	rec, _ := s.Get("sha256:abc")
	if rec.Status != state.ImageStatusDeleted {
		t.Errorf("status: got %q, want %q", rec.Status, state.ImageStatusDeleted)
	}
}

func TestMarkDeleted_NotFound(t *testing.T) {
	s := newStore(t)
	err := s.MarkDeleted("sha256:notexist")
	if !errors.Is(err, state.ErrImageNotFound) {
		t.Errorf("want ErrImageNotFound, got %v", err)
	}
}

// ── MarkDeletedOutOfBand ──────────────────────────────────────────────────────

func TestMarkDeletedOutOfBand_TransitionsStatus(t *testing.T) {
	s := newStore(t)
	s.Register(params("sha256:abc", nil, state.OriginOutOfBand))

	if err := s.MarkDeletedOutOfBand("sha256:abc"); err != nil {
		t.Fatalf("MarkDeletedOutOfBand: %v", err)
	}

	rec, _ := s.Get("sha256:abc")
	if rec.Status != state.ImageStatusDeletedOutOfBand {
		t.Errorf("status: got %q, want %q", rec.Status, state.ImageStatusDeletedOutOfBand)
	}
}

func TestMarkDeletedOutOfBand_NotFound(t *testing.T) {
	s := newStore(t)
	err := s.MarkDeletedOutOfBand("sha256:ghost")
	if !errors.Is(err, state.ErrImageNotFound) {
		t.Errorf("want ErrImageNotFound, got %v", err)
	}
}

// ── MarkErrored ───────────────────────────────────────────────────────────────

func TestMarkErrored_SetsStatusAndMsg(t *testing.T) {
	s := newStore(t)
	s.Register(params("sha256:abc", nil, state.OriginInBand))

	if err := s.MarkErrored("sha256:abc", "pull timed out"); err != nil {
		t.Fatalf("MarkErrored: %v", err)
	}

	rec, _ := s.Get("sha256:abc")
	if rec.Status != state.ImageStatusErrored {
		t.Errorf("status: got %q, want %q", rec.Status, state.ImageStatusErrored)
	}
	if rec.ErrMsg != "pull timed out" {
		t.Errorf("ErrMsg: got %q, want %q", rec.ErrMsg, "pull timed out")
	}
}

func TestMarkErrored_EmptyMsg_UsesPlaceholder(t *testing.T) {
	s := newStore(t)
	s.Register(params("sha256:abc", nil, state.OriginInBand))
	_ = s.MarkErrored("sha256:abc", "")

	rec, _ := s.Get("sha256:abc")
	if rec.ErrMsg == "" {
		t.Error("ErrMsg should have a placeholder, not be empty")
	}
}

func TestMarkErrored_NotFound(t *testing.T) {
	err := newStore(t).MarkErrored("sha256:ghost", "oops")
	if !errors.Is(err, state.ErrImageNotFound) {
		t.Errorf("want ErrImageNotFound, got %v", err)
	}
}

// ── MarkPresent ───────────────────────────────────────────────────────────────

func TestMarkPresent_RestoresFromDeleted(t *testing.T) {
	s := newStore(t)
	s.Register(params("sha256:abc", nil, state.OriginInBand))
	_ = s.MarkDeleted("sha256:abc")

	if err := s.MarkPresent("sha256:abc"); err != nil {
		t.Fatalf("MarkPresent: %v", err)
	}
	rec, _ := s.Get("sha256:abc")
	if rec.Status != state.ImageStatusPresent {
		t.Errorf("status: got %q, want Present", rec.Status)
	}
	if rec.ErrMsg != "" {
		t.Errorf("ErrMsg should be cleared, got %q", rec.ErrMsg)
	}
}

func TestMarkPresent_NotFound(t *testing.T) {
	err := newStore(t).MarkPresent("sha256:ghost")
	if !errors.Is(err, state.ErrImageNotFound) {
		t.Errorf("want ErrImageNotFound, got %v", err)
	}
}

// ── Get ───────────────────────────────────────────────────────────────────────

func TestGet_ReturnsRecord(t *testing.T) {
	s := newStore(t)
	s.Register(params("sha256:abc", []string{"img:latest"}, state.OriginInBand))

	rec, ok := s.Get("sha256:abc")
	if !ok {
		t.Fatal("expected record to be found")
	}
	if rec.DockerID != "sha256:abc" {
		t.Errorf("DockerID: got %q", rec.DockerID)
	}
}

func TestGet_UnknownID_ReturnsFalse(t *testing.T) {
	_, ok := newStore(t).Get("sha256:unknown")
	if ok {
		t.Error("expected ok=false for unknown ID")
	}
}

// ── All ───────────────────────────────────────────────────────────────────────

func TestAll_ReturnsAllRecords(t *testing.T) {
	s := newStore(t)
	for i := range 5 {
		s.Register(params(fmt.Sprintf("sha256:img%d", i), nil, state.OriginInBand))
	}

	all := s.All()
	if len(all) != 5 {
		t.Errorf("All: got %d, want 5", len(all))
	}
}

func TestAll_ReturnsCopies(t *testing.T) {
	s := newStore(t)
	s.Register(params("sha256:abc", []string{"tag:1"}, state.OriginInBand))

	all := s.All()
	if len(all) == 0 {
		t.Fatal("expected records")
	}
	all[0].ShortID = "MUTATED"

	rec, _ := s.Get("sha256:abc")
	if rec.ShortID == "MUTATED" {
		t.Error("mutating All() result affected store")
	}
}

// ── ByStatus ─────────────────────────────────────────────────────────────────

func TestByStatus_FiltersCorrectly(t *testing.T) {
	s := newStore(t)
	s.Register(params("sha256:a", nil, state.OriginInBand))
	s.Register(params("sha256:b", nil, state.OriginInBand))
	s.Register(params("sha256:c", nil, state.OriginOutOfBand))

	_ = s.MarkDeleted("sha256:b")
	_ = s.MarkDeletedOutOfBand("sha256:c")

	present := s.ByStatus(state.ImageStatusPresent)
	if len(present) != 1 {
		t.Errorf("present: got %d, want 1", len(present))
	}
	deleted := s.ByStatus(state.ImageStatusDeleted)
	if len(deleted) != 1 {
		t.Errorf("deleted: got %d, want 1", len(deleted))
	}
	oob := s.ByStatus(state.ImageStatusDeletedOutOfBand)
	if len(oob) != 1 {
		t.Errorf("deleted_out_of_band: got %d, want 1", len(oob))
	}
}

// ── Reconcile ────────────────────────────────────────────────────────────────

func TestReconcile_MarksAbsentPresentsAsDeletedOutOfBand(t *testing.T) {
	s := newStore(t)
	s.Register(params("sha256:keep", nil, state.OriginInBand))
	s.Register(params("sha256:gone", nil, state.OriginOutOfBand))
	s.Register(params("sha256:also-gone", nil, state.OriginInBand))

	affected := s.Reconcile([]string{"sha256:keep"})

	if len(affected) != 2 {
		t.Errorf("affected: got %d, want 2 (gone + also-gone)", len(affected))
	}

	keep, _ := s.Get("sha256:keep")
	if keep.Status != state.ImageStatusPresent {
		t.Errorf("keep: status should remain Present, got %q", keep.Status)
	}

	for _, id := range []string{"sha256:gone", "sha256:also-gone"} {
		rec, _ := s.Get(id)
		if rec.Status != state.ImageStatusDeletedOutOfBand {
			t.Errorf("%s: status should be DeletedOutOfBand, got %q", id, rec.Status)
		}
	}
}

func TestReconcile_DoesNotTouchAlreadyDeletedRecords(t *testing.T) {
	s := newStore(t)
	s.Register(params("sha256:gone", nil, state.OriginInBand))
	_ = s.MarkDeleted("sha256:gone") // already deleted in-band

	affected := s.Reconcile([]string{}) // empty live set
	if len(affected) != 0 {
		t.Errorf("already-deleted record should not be in affected list, got %v", affected)
	}

	rec, _ := s.Get("sha256:gone")
	if rec.Status != state.ImageStatusDeleted {
		t.Errorf("status should remain Deleted, got %q", rec.Status)
	}
}

func TestReconcile_EmptyLiveSet_MarkAllPresentAsOutOfBand(t *testing.T) {
	s := newStore(t)
	for i := range 4 {
		s.Register(params(fmt.Sprintf("sha256:img%d", i), nil, state.OriginInBand))
	}

	affected := s.Reconcile(nil)
	if len(affected) != 4 {
		t.Errorf("all 4 should be marked out-of-band, got %d", len(affected))
	}
}

func TestReconcile_AllLive_NoChanges(t *testing.T) {
	s := newStore(t)
	ids := []string{"sha256:a", "sha256:b", "sha256:c"}
	for _, id := range ids {
		s.Register(params(id, nil, state.OriginInBand))
	}

	affected := s.Reconcile(ids)
	if len(affected) != 0 {
		t.Errorf("no images should be affected when all are live, got %v", affected)
	}
}

// ── UpdatedAt ─────────────────────────────────────────────────────────────────

func TestUpdatedAt_ChangesOnTransition(t *testing.T) {
	s := newStore(t)
	rec := s.Register(params("sha256:abc", nil, state.OriginInBand))
	before := rec.UpdatedAt

	time.Sleep(2 * time.Millisecond)
	_ = s.MarkDeleted("sha256:abc")

	after, _ := s.Get("sha256:abc")
	if !after.UpdatedAt.After(before) {
		t.Errorf("UpdatedAt should advance on transition: before=%v after=%v",
			before, after.UpdatedAt)
	}
}

// ── Concurrency ───────────────────────────────────────────────────────────────

func TestStore_ConcurrentRegistersAndReads(t *testing.T) {
	s := newStore(t)
	const n = 200

	var wg sync.WaitGroup
	for i := range n {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("sha256:img%06d", i)
			s.Register(params(id, []string{fmt.Sprintf("img:%d", i)}, state.OriginInBand))
		}()
	}
	wg.Wait()

	if s.Size() != n {
		t.Errorf("store size: got %d, want %d", s.Size(), n)
	}

	// Concurrent reads while more transitions happen.
	for i := range n / 2 {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("sha256:img%06d", i)
			_ = s.MarkDeleted(id)
		}()
	}
	for range n / 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.All()
		}()
	}
	wg.Wait()

	deleted := s.ByStatus(state.ImageStatusDeleted)
	if len(deleted) != n/2 {
		t.Errorf("deleted: got %d, want %d", len(deleted), n/2)
	}
}
