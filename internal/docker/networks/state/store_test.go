package dockernetworkstate_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	state "dokoko.ai/dokoko/internal/docker/networks/state"
	"dokoko.ai/dokoko/pkg/logger"
)

func newNetworkStore(t *testing.T) *state.NetworkStore {
	t.Helper()
	return state.NewNetworkStore(logger.New(logger.LevelSilent))
}

func nparams(dockerID, name, driver string, origin state.NetworkOrigin) state.RegisterNetworkParams {
	return state.RegisterNetworkParams{
		DockerID:   dockerID,
		Name:       name,
		Driver:     driver,
		Scope:      "local",
		Internal:   false,
		Attachable: true,
		EnableIPv6: false,
		Origin:     origin,
	}
}

// ── Register ─────────────────────────────────────────────────────────────────

func TestNetworkRegister_NewRecord_StatusPresent(t *testing.T) {
	s := newNetworkStore(t)
	rec := s.Register(nparams("abc123def456", "my-net", "bridge", state.NetworkOriginInBand))

	if rec.Status != state.NetworkStatusPresent {
		t.Errorf("status: got %q, want %q", rec.Status, state.NetworkStatusPresent)
	}
	if rec.DockerID != "abc123def456" {
		t.Errorf("DockerID: got %q", rec.DockerID)
	}
	if rec.ShortID != "abc123def456" {
		t.Errorf("ShortID: got %q, want first 12 chars", rec.ShortID)
	}
	if rec.Name != "my-net" {
		t.Errorf("Name: got %q", rec.Name)
	}
	if rec.Origin != state.NetworkOriginInBand {
		t.Errorf("Origin: got %q, want %q", rec.Origin, state.NetworkOriginInBand)
	}
	if rec.RegisteredAt.IsZero() {
		t.Error("RegisteredAt should not be zero")
	}
}

func TestNetworkRegister_ShortID_TruncatesAt12(t *testing.T) {
	s := newNetworkStore(t)
	rec := s.Register(nparams("abcdef1234567890deadbeef", "net", "bridge", state.NetworkOriginInBand))
	if rec.ShortID != "abcdef123456" {
		t.Errorf("ShortID: got %q, want %q", rec.ShortID, "abcdef123456")
	}
}

func TestNetworkRegister_Upsert_UpdatesMutableFields(t *testing.T) {
	s := newNetworkStore(t)
	s.Register(nparams("net-id-1", "old-name", "bridge", state.NetworkOriginInBand))

	updated := s.Register(nparams("net-id-1", "new-name", "overlay", state.NetworkOriginInBand))
	if updated.Name != "new-name" {
		t.Errorf("Name: got %q, want new-name", updated.Name)
	}
	if updated.Driver != "overlay" {
		t.Errorf("Driver: got %q, want overlay", updated.Driver)
	}
	if s.Size() != 1 {
		t.Errorf("store size: got %d, want 1 (upsert not insert)", s.Size())
	}
}

func TestNetworkRegister_Upsert_PreservesRegisteredAt(t *testing.T) {
	s := newNetworkStore(t)
	first := s.Register(nparams("id1", "net", "bridge", state.NetworkOriginInBand))
	time.Sleep(2 * time.Millisecond)
	second := s.Register(nparams("id1", "net-renamed", "bridge", state.NetworkOriginInBand))

	if !second.RegisteredAt.Equal(first.RegisteredAt) {
		t.Errorf("RegisteredAt changed on upsert: first=%v second=%v",
			first.RegisteredAt, second.RegisteredAt)
	}
}

func TestNetworkRegister_ResetsStatusToPresent(t *testing.T) {
	s := newNetworkStore(t)
	s.Register(nparams("id1", "net", "bridge", state.NetworkOriginInBand))
	_ = s.MarkDeleted("id1")

	rec := s.Register(nparams("id1", "net", "bridge", state.NetworkOriginInBand))
	if rec.Status != state.NetworkStatusPresent {
		t.Errorf("re-register should restore to Present, got %q", rec.Status)
	}
}

func TestNetworkRegister_UpgradesOriginOutOfBandToInBand(t *testing.T) {
	s := newNetworkStore(t)
	s.Register(nparams("id1", "net", "bridge", state.NetworkOriginOutOfBand))

	updated := s.Register(nparams("id1", "net", "bridge", state.NetworkOriginInBand))
	if updated.Origin != state.NetworkOriginInBand {
		t.Errorf("Origin: want InBand after upgrade, got %q", updated.Origin)
	}
}

func TestNetworkRegister_DoesNotDowngradeOriginInBandToOutOfBand(t *testing.T) {
	s := newNetworkStore(t)
	s.Register(nparams("id1", "net", "bridge", state.NetworkOriginInBand))

	updated := s.Register(nparams("id1", "net", "bridge", state.NetworkOriginOutOfBand))
	if updated.Origin != state.NetworkOriginInBand {
		t.Errorf("Origin: InBand should not be downgraded to OutOfBand, got %q", updated.Origin)
	}
}

// ── MarkDeleted ───────────────────────────────────────────────────────────────

func TestNetworkMarkDeleted_TransitionsToDeleted(t *testing.T) {
	s := newNetworkStore(t)
	s.Register(nparams("id1", "net", "bridge", state.NetworkOriginInBand))

	if err := s.MarkDeleted("id1"); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}
	rec, _ := s.Get("id1")
	if rec.Status != state.NetworkStatusDeleted {
		t.Errorf("status: got %q, want %q", rec.Status, state.NetworkStatusDeleted)
	}
}

func TestNetworkMarkDeleted_NotFound(t *testing.T) {
	err := newNetworkStore(t).MarkDeleted("ghost-id")
	if !errors.Is(err, state.ErrNetworkNotFound) {
		t.Errorf("want ErrNetworkNotFound, got %v", err)
	}
}

// ── MarkDeletedOutOfBand ──────────────────────────────────────────────────────

func TestNetworkMarkDeletedOutOfBand_TransitionsStatus(t *testing.T) {
	s := newNetworkStore(t)
	s.Register(nparams("id1", "net", "bridge", state.NetworkOriginOutOfBand))

	if err := s.MarkDeletedOutOfBand("id1"); err != nil {
		t.Fatalf("MarkDeletedOutOfBand: %v", err)
	}
	rec, _ := s.Get("id1")
	if rec.Status != state.NetworkStatusDeletedOutOfBand {
		t.Errorf("status: got %q, want %q", rec.Status, state.NetworkStatusDeletedOutOfBand)
	}
}

func TestNetworkMarkDeletedOutOfBand_NotFound(t *testing.T) {
	err := newNetworkStore(t).MarkDeletedOutOfBand("ghost-id")
	if !errors.Is(err, state.ErrNetworkNotFound) {
		t.Errorf("want ErrNetworkNotFound, got %v", err)
	}
}

// ── MarkErrored ───────────────────────────────────────────────────────────────

func TestNetworkMarkErrored_SetsStatusAndMsg(t *testing.T) {
	s := newNetworkStore(t)
	s.Register(nparams("id1", "net", "bridge", state.NetworkOriginInBand))

	if err := s.MarkErrored("id1", "subnet conflict"); err != nil {
		t.Fatalf("MarkErrored: %v", err)
	}
	rec, _ := s.Get("id1")
	if rec.Status != state.NetworkStatusErrored {
		t.Errorf("status: got %q, want Errored", rec.Status)
	}
	if rec.ErrMsg != "subnet conflict" {
		t.Errorf("ErrMsg: got %q, want %q", rec.ErrMsg, "subnet conflict")
	}
}

func TestNetworkMarkErrored_EmptyMsg_UsesPlaceholder(t *testing.T) {
	s := newNetworkStore(t)
	s.Register(nparams("id1", "net", "bridge", state.NetworkOriginInBand))
	_ = s.MarkErrored("id1", "")

	rec, _ := s.Get("id1")
	if rec.ErrMsg == "" {
		t.Error("ErrMsg should have a placeholder, not be empty")
	}
}

func TestNetworkMarkErrored_NotFound(t *testing.T) {
	err := newNetworkStore(t).MarkErrored("ghost", "oops")
	if !errors.Is(err, state.ErrNetworkNotFound) {
		t.Errorf("want ErrNetworkNotFound, got %v", err)
	}
}

// ── MarkPresent ───────────────────────────────────────────────────────────────

func TestNetworkMarkPresent_RestoresFromDeleted(t *testing.T) {
	s := newNetworkStore(t)
	s.Register(nparams("id1", "net", "bridge", state.NetworkOriginInBand))
	_ = s.MarkDeleted("id1")

	if err := s.MarkPresent("id1"); err != nil {
		t.Fatalf("MarkPresent: %v", err)
	}
	rec, _ := s.Get("id1")
	if rec.Status != state.NetworkStatusPresent {
		t.Errorf("status: got %q, want Present", rec.Status)
	}
	if rec.ErrMsg != "" {
		t.Errorf("ErrMsg should be cleared, got %q", rec.ErrMsg)
	}
}

func TestNetworkMarkPresent_NotFound(t *testing.T) {
	err := newNetworkStore(t).MarkPresent("ghost")
	if !errors.Is(err, state.ErrNetworkNotFound) {
		t.Errorf("want ErrNetworkNotFound, got %v", err)
	}
}

// ── ByStatus ─────────────────────────────────────────────────────────────────

func TestNetworkByStatus_FiltersCorrectly(t *testing.T) {
	s := newNetworkStore(t)
	s.Register(nparams("a", "net-a", "bridge", state.NetworkOriginInBand))
	s.Register(nparams("b", "net-b", "bridge", state.NetworkOriginInBand))
	s.Register(nparams("c", "net-c", "bridge", state.NetworkOriginOutOfBand))

	_ = s.MarkDeleted("b")
	_ = s.MarkDeletedOutOfBand("c")

	if len(s.ByStatus(state.NetworkStatusPresent)) != 1 {
		t.Errorf("present: want 1, got %d", len(s.ByStatus(state.NetworkStatusPresent)))
	}
	if len(s.ByStatus(state.NetworkStatusDeleted)) != 1 {
		t.Errorf("deleted: want 1, got %d", len(s.ByStatus(state.NetworkStatusDeleted)))
	}
	if len(s.ByStatus(state.NetworkStatusDeletedOutOfBand)) != 1 {
		t.Errorf("deleted_out_of_band: want 1, got %d",
			len(s.ByStatus(state.NetworkStatusDeletedOutOfBand)))
	}
}

// ── Reconcile ────────────────────────────────────────────────────────────────

func TestNetworkReconcile_MarksAbsentPresentsAsDeletedOutOfBand(t *testing.T) {
	s := newNetworkStore(t)
	s.Register(nparams("keep-id", "keep", "bridge", state.NetworkOriginInBand))
	s.Register(nparams("gone-id", "gone", "bridge", state.NetworkOriginOutOfBand))
	s.Register(nparams("also-gone-id", "also-gone", "bridge", state.NetworkOriginInBand))

	affected := s.Reconcile([]string{"keep-id"})
	if len(affected) != 2 {
		t.Errorf("affected: got %d, want 2", len(affected))
	}

	keep, _ := s.Get("keep-id")
	if keep.Status != state.NetworkStatusPresent {
		t.Errorf("keep: should remain Present, got %q", keep.Status)
	}

	for _, id := range []string{"gone-id", "also-gone-id"} {
		rec, _ := s.Get(id)
		if rec.Status != state.NetworkStatusDeletedOutOfBand {
			t.Errorf("%s: should be DeletedOutOfBand, got %q", id, rec.Status)
		}
	}
}

func TestNetworkReconcile_DoesNotTouchAlreadyDeletedRecords(t *testing.T) {
	s := newNetworkStore(t)
	s.Register(nparams("id1", "net", "bridge", state.NetworkOriginInBand))
	_ = s.MarkDeleted("id1")

	affected := s.Reconcile([]string{})
	if len(affected) != 0 {
		t.Errorf("already-deleted record should not be in affected list, got %v", affected)
	}
	rec, _ := s.Get("id1")
	if rec.Status != state.NetworkStatusDeleted {
		t.Errorf("status should remain Deleted, got %q", rec.Status)
	}
}

func TestNetworkReconcile_EmptyLiveSet_MarkAllPresentAsOutOfBand(t *testing.T) {
	s := newNetworkStore(t)
	for i := range 4 {
		s.Register(nparams(fmt.Sprintf("id%d", i), fmt.Sprintf("net%d", i), "bridge", state.NetworkOriginInBand))
	}
	affected := s.Reconcile(nil)
	if len(affected) != 4 {
		t.Errorf("all 4 should be marked out-of-band, got %d", len(affected))
	}
}

// ── UpdatedAt ─────────────────────────────────────────────────────────────────

func TestNetworkUpdatedAt_ChangesOnTransition(t *testing.T) {
	s := newNetworkStore(t)
	rec := s.Register(nparams("id1", "net", "bridge", state.NetworkOriginInBand))
	before := rec.UpdatedAt

	time.Sleep(2 * time.Millisecond)
	_ = s.MarkDeleted("id1")

	after, _ := s.Get("id1")
	if !after.UpdatedAt.After(before) {
		t.Errorf("UpdatedAt should advance on transition: before=%v after=%v",
			before, after.UpdatedAt)
	}
}

// ── Concurrency ───────────────────────────────────────────────────────────────

func TestNetworkStore_ConcurrentRegistersAndReads(t *testing.T) {
	s := newNetworkStore(t)
	const n = 200

	var wg sync.WaitGroup
	for i := range n {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Register(nparams(
				fmt.Sprintf("net-id-%06d", i),
				fmt.Sprintf("net-%d", i),
				"bridge",
				state.NetworkOriginInBand,
			))
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
			_ = s.MarkDeleted(fmt.Sprintf("net-id-%06d", i))
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

	deleted := s.ByStatus(state.NetworkStatusDeleted)
	if len(deleted) != n/2 {
		t.Errorf("deleted: got %d, want %d", len(deleted), n/2)
	}
}
