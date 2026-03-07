package dockervolumestate_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	state "dokoko.ai/dokoko/internal/docker/volumes/state"
	"dokoko.ai/dokoko/pkg/logger"
)

func newVolumeStore(t *testing.T) *state.VolumeStore {
	t.Helper()
	return state.NewVolumeStore(logger.New(logger.LevelSilent))
}

func vparams(name, driver string, origin state.VolumeOrigin) state.RegisterVolumeParams {
	return state.RegisterVolumeParams{
		Name:       name,
		Driver:     driver,
		Mountpoint: "/var/lib/docker/volumes/" + name + "/_data",
		Scope:      "local",
		Labels:     map[string]string{"app": "test"},
		Options:    map[string]string{"type": "none"},
		Origin:     origin,
	}
}

// ── Register ─────────────────────────────────────────────────────────────────

func TestVolumeRegister_NewRecord_StatusPresent(t *testing.T) {
	s := newVolumeStore(t)
	rec := s.Register(vparams("data", "local", state.VolumeOriginInBand))

	if rec.Status != state.VolumeStatusPresent {
		t.Errorf("status: got %q, want %q", rec.Status, state.VolumeStatusPresent)
	}
	if rec.Name != "data" {
		t.Errorf("Name: got %q, want %q", rec.Name, "data")
	}
	if rec.Driver != "local" {
		t.Errorf("Driver: got %q, want %q", rec.Driver, "local")
	}
	if rec.Origin != state.VolumeOriginInBand {
		t.Errorf("Origin: got %q, want %q", rec.Origin, state.VolumeOriginInBand)
	}
	if rec.RegisteredAt.IsZero() {
		t.Error("RegisteredAt should not be zero")
	}
}

func TestVolumeRegister_Upsert_UpdatesMutableFields(t *testing.T) {
	s := newVolumeStore(t)
	s.Register(vparams("vol", "local", state.VolumeOriginInBand))

	updated := s.Register(state.RegisterVolumeParams{
		Name:   "vol",
		Driver: "nfs",
		Origin: state.VolumeOriginInBand,
	})

	if updated.Driver != "nfs" {
		t.Errorf("Driver: got %q, want %q", updated.Driver, "nfs")
	}
	if s.Size() != 1 {
		t.Errorf("store size: got %d, want 1 (upsert not insert)", s.Size())
	}
}

func TestVolumeRegister_Upsert_PreservesRegisteredAt(t *testing.T) {
	s := newVolumeStore(t)
	first := s.Register(vparams("vol", "local", state.VolumeOriginInBand))
	time.Sleep(2 * time.Millisecond)
	second := s.Register(vparams("vol", "overlay", state.VolumeOriginInBand))

	if !second.RegisteredAt.Equal(first.RegisteredAt) {
		t.Errorf("RegisteredAt changed on upsert: first=%v second=%v",
			first.RegisteredAt, second.RegisteredAt)
	}
}

func TestVolumeRegister_ResetsStatusToPresent(t *testing.T) {
	s := newVolumeStore(t)
	s.Register(vparams("vol", "local", state.VolumeOriginInBand))
	_ = s.MarkDeleted("vol")

	rec := s.Register(vparams("vol", "local", state.VolumeOriginInBand))
	if rec.Status != state.VolumeStatusPresent {
		t.Errorf("re-register should restore to Present, got %q", rec.Status)
	}
}

func TestVolumeRegister_UpgradesOriginOutOfBandToInBand(t *testing.T) {
	s := newVolumeStore(t)
	s.Register(vparams("vol", "local", state.VolumeOriginOutOfBand))

	updated := s.Register(vparams("vol", "local", state.VolumeOriginInBand))
	if updated.Origin != state.VolumeOriginInBand {
		t.Errorf("Origin: want InBand after upgrade, got %q", updated.Origin)
	}
}

func TestVolumeRegister_DoesNotDowngradeOriginInBandToOutOfBand(t *testing.T) {
	s := newVolumeStore(t)
	s.Register(vparams("vol", "local", state.VolumeOriginInBand))

	updated := s.Register(vparams("vol", "local", state.VolumeOriginOutOfBand))
	if updated.Origin != state.VolumeOriginInBand {
		t.Errorf("Origin: InBand should not be downgraded to OutOfBand, got %q", updated.Origin)
	}
}

func TestVolumeRegister_ReturnedRecordIsACopy(t *testing.T) {
	s := newVolumeStore(t)
	rec := s.Register(vparams("vol", "local", state.VolumeOriginInBand))

	rec.Driver = "MUTATED"
	stored, _ := s.Get("vol")
	if stored.Driver == "MUTATED" {
		t.Error("mutation of returned record affected store copy")
	}
}

// ── MarkDeleted ───────────────────────────────────────────────────────────────

func TestVolumeMarkDeleted_TransitionsToDeleted(t *testing.T) {
	s := newVolumeStore(t)
	s.Register(vparams("vol", "local", state.VolumeOriginInBand))

	if err := s.MarkDeleted("vol"); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}
	rec, _ := s.Get("vol")
	if rec.Status != state.VolumeStatusDeleted {
		t.Errorf("status: got %q, want %q", rec.Status, state.VolumeStatusDeleted)
	}
}

func TestVolumeMarkDeleted_NotFound(t *testing.T) {
	err := newVolumeStore(t).MarkDeleted("ghost")
	if !errors.Is(err, state.ErrVolumeNotFound) {
		t.Errorf("want ErrVolumeNotFound, got %v", err)
	}
}

// ── MarkDeletedOutOfBand ──────────────────────────────────────────────────────

func TestVolumeMarkDeletedOutOfBand_TransitionsStatus(t *testing.T) {
	s := newVolumeStore(t)
	s.Register(vparams("vol", "local", state.VolumeOriginOutOfBand))

	if err := s.MarkDeletedOutOfBand("vol"); err != nil {
		t.Fatalf("MarkDeletedOutOfBand: %v", err)
	}
	rec, _ := s.Get("vol")
	if rec.Status != state.VolumeStatusDeletedOutOfBand {
		t.Errorf("status: got %q, want %q", rec.Status, state.VolumeStatusDeletedOutOfBand)
	}
}

func TestVolumeMarkDeletedOutOfBand_NotFound(t *testing.T) {
	err := newVolumeStore(t).MarkDeletedOutOfBand("ghost")
	if !errors.Is(err, state.ErrVolumeNotFound) {
		t.Errorf("want ErrVolumeNotFound, got %v", err)
	}
}

// ── MarkErrored ───────────────────────────────────────────────────────────────

func TestVolumeMarkErrored_SetsStatusAndMsg(t *testing.T) {
	s := newVolumeStore(t)
	s.Register(vparams("vol", "local", state.VolumeOriginInBand))

	if err := s.MarkErrored("vol", "driver busy"); err != nil {
		t.Fatalf("MarkErrored: %v", err)
	}
	rec, _ := s.Get("vol")
	if rec.Status != state.VolumeStatusErrored {
		t.Errorf("status: got %q, want Errored", rec.Status)
	}
	if rec.ErrMsg != "driver busy" {
		t.Errorf("ErrMsg: got %q, want %q", rec.ErrMsg, "driver busy")
	}
}

func TestVolumeMarkErrored_EmptyMsg_UsesPlaceholder(t *testing.T) {
	s := newVolumeStore(t)
	s.Register(vparams("vol", "local", state.VolumeOriginInBand))
	_ = s.MarkErrored("vol", "")

	rec, _ := s.Get("vol")
	if rec.ErrMsg == "" {
		t.Error("ErrMsg should have a placeholder, not be empty")
	}
}

func TestVolumeMarkErrored_NotFound(t *testing.T) {
	err := newVolumeStore(t).MarkErrored("ghost", "oops")
	if !errors.Is(err, state.ErrVolumeNotFound) {
		t.Errorf("want ErrVolumeNotFound, got %v", err)
	}
}

// ── MarkPresent ───────────────────────────────────────────────────────────────

func TestVolumeMarkPresent_RestoresFromDeleted(t *testing.T) {
	s := newVolumeStore(t)
	s.Register(vparams("vol", "local", state.VolumeOriginInBand))
	_ = s.MarkDeleted("vol")

	if err := s.MarkPresent("vol"); err != nil {
		t.Fatalf("MarkPresent: %v", err)
	}
	rec, _ := s.Get("vol")
	if rec.Status != state.VolumeStatusPresent {
		t.Errorf("status: got %q, want Present", rec.Status)
	}
	if rec.ErrMsg != "" {
		t.Errorf("ErrMsg should be cleared, got %q", rec.ErrMsg)
	}
}

func TestVolumeMarkPresent_NotFound(t *testing.T) {
	err := newVolumeStore(t).MarkPresent("ghost")
	if !errors.Is(err, state.ErrVolumeNotFound) {
		t.Errorf("want ErrVolumeNotFound, got %v", err)
	}
}

// ── Get ───────────────────────────────────────────────────────────────────────

func TestVolumeGet_ReturnsRecord(t *testing.T) {
	s := newVolumeStore(t)
	s.Register(vparams("vol", "local", state.VolumeOriginInBand))

	rec, ok := s.Get("vol")
	if !ok {
		t.Fatal("expected record to be found")
	}
	if rec.Name != "vol" {
		t.Errorf("Name: got %q, want %q", rec.Name, "vol")
	}
}

func TestVolumeGet_UnknownName_ReturnsFalse(t *testing.T) {
	_, ok := newVolumeStore(t).Get("no-such-vol")
	if ok {
		t.Error("expected ok=false for unknown volume")
	}
}

// ── All ───────────────────────────────────────────────────────────────────────

func TestVolumeAll_ReturnsAllRecords(t *testing.T) {
	s := newVolumeStore(t)
	for i := range 5 {
		s.Register(vparams(fmt.Sprintf("vol%d", i), "local", state.VolumeOriginInBand))
	}
	if len(s.All()) != 5 {
		t.Errorf("All: got %d, want 5", len(s.All()))
	}
}

func TestVolumeAll_ReturnsCopies(t *testing.T) {
	s := newVolumeStore(t)
	s.Register(vparams("vol", "local", state.VolumeOriginInBand))

	all := s.All()
	all[0].Driver = "MUTATED"

	rec, _ := s.Get("vol")
	if rec.Driver == "MUTATED" {
		t.Error("mutating All() result affected store")
	}
}

// ── ByStatus ─────────────────────────────────────────────────────────────────

func TestVolumeByStatus_FiltersCorrectly(t *testing.T) {
	s := newVolumeStore(t)
	s.Register(vparams("a", "local", state.VolumeOriginInBand))
	s.Register(vparams("b", "local", state.VolumeOriginInBand))
	s.Register(vparams("c", "local", state.VolumeOriginOutOfBand))

	_ = s.MarkDeleted("b")
	_ = s.MarkDeletedOutOfBand("c")

	if len(s.ByStatus(state.VolumeStatusPresent)) != 1 {
		t.Errorf("present: want 1, got %d", len(s.ByStatus(state.VolumeStatusPresent)))
	}
	if len(s.ByStatus(state.VolumeStatusDeleted)) != 1 {
		t.Errorf("deleted: want 1, got %d", len(s.ByStatus(state.VolumeStatusDeleted)))
	}
	if len(s.ByStatus(state.VolumeStatusDeletedOutOfBand)) != 1 {
		t.Errorf("deleted_out_of_band: want 1, got %d",
			len(s.ByStatus(state.VolumeStatusDeletedOutOfBand)))
	}
}

// ── Reconcile ────────────────────────────────────────────────────────────────

func TestVolumeReconcile_MarksAbsentPresentAsDeletedOutOfBand(t *testing.T) {
	s := newVolumeStore(t)
	s.Register(vparams("keep", "local", state.VolumeOriginInBand))
	s.Register(vparams("gone", "local", state.VolumeOriginOutOfBand))
	s.Register(vparams("also-gone", "local", state.VolumeOriginInBand))

	affected := s.Reconcile([]string{"keep"})
	if len(affected) != 2 {
		t.Errorf("affected: got %d, want 2", len(affected))
	}

	keep, _ := s.Get("keep")
	if keep.Status != state.VolumeStatusPresent {
		t.Errorf("keep: status should remain Present, got %q", keep.Status)
	}

	for _, name := range []string{"gone", "also-gone"} {
		rec, _ := s.Get(name)
		if rec.Status != state.VolumeStatusDeletedOutOfBand {
			t.Errorf("%s: status should be DeletedOutOfBand, got %q", name, rec.Status)
		}
	}
}

func TestVolumeReconcile_DoesNotTouchAlreadyDeletedRecords(t *testing.T) {
	s := newVolumeStore(t)
	s.Register(vparams("vol", "local", state.VolumeOriginInBand))
	_ = s.MarkDeleted("vol")

	affected := s.Reconcile([]string{})
	if len(affected) != 0 {
		t.Errorf("already-deleted record should not be in affected list, got %v", affected)
	}
	rec, _ := s.Get("vol")
	if rec.Status != state.VolumeStatusDeleted {
		t.Errorf("status should remain Deleted, got %q", rec.Status)
	}
}

func TestVolumeReconcile_EmptyLiveSet_MarkAllPresentAsOutOfBand(t *testing.T) {
	s := newVolumeStore(t)
	for i := range 4 {
		s.Register(vparams(fmt.Sprintf("vol%d", i), "local", state.VolumeOriginInBand))
	}
	affected := s.Reconcile(nil)
	if len(affected) != 4 {
		t.Errorf("all 4 should be marked out-of-band, got %d", len(affected))
	}
}

func TestVolumeReconcile_AllLive_NoChanges(t *testing.T) {
	s := newVolumeStore(t)
	names := []string{"a", "b", "c"}
	for _, n := range names {
		s.Register(vparams(n, "local", state.VolumeOriginInBand))
	}
	affected := s.Reconcile(names)
	if len(affected) != 0 {
		t.Errorf("no volumes should be affected, got %v", affected)
	}
}

// ── UpdatedAt ─────────────────────────────────────────────────────────────────

func TestVolumeUpdatedAt_ChangesOnTransition(t *testing.T) {
	s := newVolumeStore(t)
	rec := s.Register(vparams("vol", "local", state.VolumeOriginInBand))
	before := rec.UpdatedAt

	time.Sleep(2 * time.Millisecond)
	_ = s.MarkDeleted("vol")

	after, _ := s.Get("vol")
	if !after.UpdatedAt.After(before) {
		t.Errorf("UpdatedAt should advance on transition: before=%v after=%v",
			before, after.UpdatedAt)
	}
}

// ── Concurrency ───────────────────────────────────────────────────────────────

func TestVolumeStore_ConcurrentRegistersAndReads(t *testing.T) {
	s := newVolumeStore(t)
	const n = 200

	var wg sync.WaitGroup
	for i := range n {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Register(vparams(fmt.Sprintf("vol-%06d", i), "local", state.VolumeOriginInBand))
		}()
	}
	wg.Wait()

	if s.Size() != n {
		t.Errorf("store size: got %d, want %d", s.Size(), n)
	}

	for i := range n / 2 {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.MarkDeleted(fmt.Sprintf("vol-%06d", i))
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

	deleted := s.ByStatus(state.VolumeStatusDeleted)
	if len(deleted) != n/2 {
		t.Errorf("deleted: got %d, want %d", len(deleted), n/2)
	}
}
