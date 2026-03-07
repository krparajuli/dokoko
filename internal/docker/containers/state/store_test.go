package dockercontainerstate_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	state "dokoko.ai/dokoko/internal/docker/containers/state"
	"dokoko.ai/dokoko/pkg/logger"
)

func newContainerStore(t *testing.T) *state.ContainerStore {
	t.Helper()
	return state.NewContainerStore(logger.New(logger.LevelSilent))
}

func cparams(dockerID, name, image string, origin state.ContainerOrigin) state.RegisterContainerParams {
	return state.RegisterContainerParams{
		DockerID:     dockerID,
		Name:         name,
		Image:        image,
		ImageID:      "sha256:image" + dockerID,
		RuntimeState: "running",
		ExitCode:     0,
		NetworkMode:  "bridge",
		CreatedAt:    time.Now(),
		Origin:       origin,
	}
}

// ── Register ─────────────────────────────────────────────────────────────────

func TestContainerRegister_NewRecord_StatusPresent(t *testing.T) {
	s := newContainerStore(t)
	rec := s.Register(cparams("abc123def456789", "web", "nginx:latest", state.ContainerOriginInBand))

	if rec.Status != state.ContainerStatusPresent {
		t.Errorf("status: got %q, want %q", rec.Status, state.ContainerStatusPresent)
	}
	if rec.DockerID != "abc123def456789" {
		t.Errorf("DockerID: got %q", rec.DockerID)
	}
	if rec.ShortID != "abc123def456" {
		t.Errorf("ShortID: got %q, want %q", rec.ShortID, "abc123def456")
	}
	if rec.Name != "web" {
		t.Errorf("Name: got %q", rec.Name)
	}
	if rec.Origin != state.ContainerOriginInBand {
		t.Errorf("Origin: got %q, want %q", rec.Origin, state.ContainerOriginInBand)
	}
	if rec.RegisteredAt.IsZero() {
		t.Error("RegisteredAt should not be zero")
	}
}

func TestContainerRegister_Upsert_UpdatesMutableFields(t *testing.T) {
	s := newContainerStore(t)
	s.Register(cparams("cid1", "web", "nginx:1.0", state.ContainerOriginInBand))

	updated := s.Register(cparams("cid1", "web", "nginx:2.0", state.ContainerOriginInBand))
	if updated.Image != "nginx:2.0" {
		t.Errorf("Image: got %q, want nginx:2.0", updated.Image)
	}
	if s.Size() != 1 {
		t.Errorf("store size: got %d, want 1 (upsert not insert)", s.Size())
	}
}

func TestContainerRegister_Upsert_PreservesRegisteredAt(t *testing.T) {
	s := newContainerStore(t)
	first := s.Register(cparams("cid1", "web", "nginx:latest", state.ContainerOriginInBand))
	time.Sleep(2 * time.Millisecond)
	second := s.Register(cparams("cid1", "web", "nginx:latest", state.ContainerOriginInBand))

	if !second.RegisteredAt.Equal(first.RegisteredAt) {
		t.Errorf("RegisteredAt changed on upsert: first=%v second=%v",
			first.RegisteredAt, second.RegisteredAt)
	}
}

func TestContainerRegister_ResetsStatusToPresent(t *testing.T) {
	s := newContainerStore(t)
	s.Register(cparams("cid1", "web", "nginx:latest", state.ContainerOriginInBand))
	_ = s.MarkRemoved("cid1")

	rec := s.Register(cparams("cid1", "web", "nginx:latest", state.ContainerOriginInBand))
	if rec.Status != state.ContainerStatusPresent {
		t.Errorf("re-register should restore to Present, got %q", rec.Status)
	}
}

func TestContainerRegister_UpgradesOriginOutOfBandToInBand(t *testing.T) {
	s := newContainerStore(t)
	s.Register(cparams("cid1", "web", "nginx:latest", state.ContainerOriginOutOfBand))

	updated := s.Register(cparams("cid1", "web", "nginx:latest", state.ContainerOriginInBand))
	if updated.Origin != state.ContainerOriginInBand {
		t.Errorf("Origin: want InBand after upgrade, got %q", updated.Origin)
	}
}

func TestContainerRegister_DoesNotDowngradeOriginInBandToOutOfBand(t *testing.T) {
	s := newContainerStore(t)
	s.Register(cparams("cid1", "web", "nginx:latest", state.ContainerOriginInBand))

	updated := s.Register(cparams("cid1", "web", "nginx:latest", state.ContainerOriginOutOfBand))
	if updated.Origin != state.ContainerOriginInBand {
		t.Errorf("Origin: InBand should not be downgraded to OutOfBand, got %q", updated.Origin)
	}
}

func TestContainerRegister_ReturnedRecordIsACopy(t *testing.T) {
	s := newContainerStore(t)
	rec := s.Register(cparams("cid1", "web", "nginx:latest", state.ContainerOriginInBand))

	rec.Name = "MUTATED"
	stored, _ := s.Get("cid1")
	if stored.Name == "MUTATED" {
		t.Error("mutation of returned record affected store copy")
	}
}

// ── UpdateRuntimeState ────────────────────────────────────────────────────────

func TestContainerUpdateRuntimeState_UpdatesFields(t *testing.T) {
	s := newContainerStore(t)
	s.Register(cparams("cid1", "web", "nginx:latest", state.ContainerOriginInBand))

	if err := s.UpdateRuntimeState("cid1", "exited", 1); err != nil {
		t.Fatalf("UpdateRuntimeState: %v", err)
	}

	rec, _ := s.Get("cid1")
	if rec.RuntimeState != "exited" {
		t.Errorf("RuntimeState: got %q, want exited", rec.RuntimeState)
	}
	if rec.ExitCode != 1 {
		t.Errorf("ExitCode: got %d, want 1", rec.ExitCode)
	}
	if rec.Status != state.ContainerStatusPresent {
		t.Errorf("inventory status should not change on UpdateRuntimeState: got %q", rec.Status)
	}
}

func TestContainerUpdateRuntimeState_NotFound(t *testing.T) {
	err := newContainerStore(t).UpdateRuntimeState("ghost-id", "running", 0)
	if !errors.Is(err, state.ErrContainerNotFound) {
		t.Errorf("want ErrContainerNotFound, got %v", err)
	}
}

// ── MarkRemoved ───────────────────────────────────────────────────────────────

func TestContainerMarkRemoved_TransitionsStatus(t *testing.T) {
	s := newContainerStore(t)
	s.Register(cparams("cid1", "web", "nginx:latest", state.ContainerOriginInBand))

	if err := s.MarkRemoved("cid1"); err != nil {
		t.Fatalf("MarkRemoved: %v", err)
	}
	rec, _ := s.Get("cid1")
	if rec.Status != state.ContainerStatusRemoved {
		t.Errorf("status: got %q, want %q", rec.Status, state.ContainerStatusRemoved)
	}
}

func TestContainerMarkRemoved_NotFound(t *testing.T) {
	err := newContainerStore(t).MarkRemoved("ghost-id")
	if !errors.Is(err, state.ErrContainerNotFound) {
		t.Errorf("want ErrContainerNotFound, got %v", err)
	}
}

// ── MarkRemovedOutOfBand ───────────────────────────────────────────────────────

func TestContainerMarkRemovedOutOfBand_TransitionsStatus(t *testing.T) {
	s := newContainerStore(t)
	s.Register(cparams("cid1", "web", "nginx:latest", state.ContainerOriginOutOfBand))

	if err := s.MarkRemovedOutOfBand("cid1"); err != nil {
		t.Fatalf("MarkRemovedOutOfBand: %v", err)
	}
	rec, _ := s.Get("cid1")
	if rec.Status != state.ContainerStatusRemovedOutOfBand {
		t.Errorf("status: got %q, want %q", rec.Status, state.ContainerStatusRemovedOutOfBand)
	}
}

func TestContainerMarkRemovedOutOfBand_NotFound(t *testing.T) {
	err := newContainerStore(t).MarkRemovedOutOfBand("ghost-id")
	if !errors.Is(err, state.ErrContainerNotFound) {
		t.Errorf("want ErrContainerNotFound, got %v", err)
	}
}

// ── MarkErrored ───────────────────────────────────────────────────────────────

func TestContainerMarkErrored_SetsStatusAndMsg(t *testing.T) {
	s := newContainerStore(t)
	s.Register(cparams("cid1", "web", "nginx:latest", state.ContainerOriginInBand))

	if err := s.MarkErrored("cid1", "OOM killed"); err != nil {
		t.Fatalf("MarkErrored: %v", err)
	}
	rec, _ := s.Get("cid1")
	if rec.Status != state.ContainerStatusErrored {
		t.Errorf("status: got %q, want Errored", rec.Status)
	}
	if rec.ErrMsg != "OOM killed" {
		t.Errorf("ErrMsg: got %q, want %q", rec.ErrMsg, "OOM killed")
	}
}

func TestContainerMarkErrored_EmptyMsg_UsesPlaceholder(t *testing.T) {
	s := newContainerStore(t)
	s.Register(cparams("cid1", "web", "nginx:latest", state.ContainerOriginInBand))
	_ = s.MarkErrored("cid1", "")

	rec, _ := s.Get("cid1")
	if rec.ErrMsg == "" {
		t.Error("ErrMsg should have a placeholder, not be empty")
	}
}

func TestContainerMarkErrored_NotFound(t *testing.T) {
	err := newContainerStore(t).MarkErrored("ghost", "oops")
	if !errors.Is(err, state.ErrContainerNotFound) {
		t.Errorf("want ErrContainerNotFound, got %v", err)
	}
}

// ── MarkPresent ───────────────────────────────────────────────────────────────

func TestContainerMarkPresent_RestoresFromRemoved(t *testing.T) {
	s := newContainerStore(t)
	s.Register(cparams("cid1", "web", "nginx:latest", state.ContainerOriginInBand))
	_ = s.MarkRemoved("cid1")

	if err := s.MarkPresent("cid1"); err != nil {
		t.Fatalf("MarkPresent: %v", err)
	}
	rec, _ := s.Get("cid1")
	if rec.Status != state.ContainerStatusPresent {
		t.Errorf("status: got %q, want Present", rec.Status)
	}
	if rec.ErrMsg != "" {
		t.Errorf("ErrMsg should be cleared, got %q", rec.ErrMsg)
	}
}

func TestContainerMarkPresent_NotFound(t *testing.T) {
	err := newContainerStore(t).MarkPresent("ghost")
	if !errors.Is(err, state.ErrContainerNotFound) {
		t.Errorf("want ErrContainerNotFound, got %v", err)
	}
}

// ── ByStatus ─────────────────────────────────────────────────────────────────

func TestContainerByStatus_FiltersCorrectly(t *testing.T) {
	s := newContainerStore(t)
	s.Register(cparams("a", "web-a", "nginx:latest", state.ContainerOriginInBand))
	s.Register(cparams("b", "web-b", "nginx:latest", state.ContainerOriginInBand))
	s.Register(cparams("c", "web-c", "nginx:latest", state.ContainerOriginOutOfBand))

	_ = s.MarkRemoved("b")
	_ = s.MarkRemovedOutOfBand("c")

	if len(s.ByStatus(state.ContainerStatusPresent)) != 1 {
		t.Errorf("present: want 1, got %d", len(s.ByStatus(state.ContainerStatusPresent)))
	}
	if len(s.ByStatus(state.ContainerStatusRemoved)) != 1 {
		t.Errorf("removed: want 1, got %d", len(s.ByStatus(state.ContainerStatusRemoved)))
	}
	if len(s.ByStatus(state.ContainerStatusRemovedOutOfBand)) != 1 {
		t.Errorf("removed_out_of_band: want 1, got %d",
			len(s.ByStatus(state.ContainerStatusRemovedOutOfBand)))
	}
}

// ── Reconcile ────────────────────────────────────────────────────────────────

func TestContainerReconcile_MarksAbsentPresentsAsRemovedOutOfBand(t *testing.T) {
	s := newContainerStore(t)
	s.Register(cparams("keep-id", "web", "nginx:latest", state.ContainerOriginInBand))
	s.Register(cparams("gone-id", "db", "postgres:latest", state.ContainerOriginOutOfBand))

	affected := s.Reconcile([]string{"keep-id"})
	if len(affected) != 1 {
		t.Errorf("affected: got %d, want 1", len(affected))
	}

	keep, _ := s.Get("keep-id")
	if keep.Status != state.ContainerStatusPresent {
		t.Errorf("keep: should remain Present, got %q", keep.Status)
	}

	gone, _ := s.Get("gone-id")
	if gone.Status != state.ContainerStatusRemovedOutOfBand {
		t.Errorf("gone: should be RemovedOutOfBand, got %q", gone.Status)
	}
}

func TestContainerReconcile_DoesNotTouchAlreadyRemovedRecords(t *testing.T) {
	s := newContainerStore(t)
	s.Register(cparams("cid1", "web", "nginx:latest", state.ContainerOriginInBand))
	_ = s.MarkRemoved("cid1")

	affected := s.Reconcile([]string{})
	if len(affected) != 0 {
		t.Errorf("already-removed record should not be affected, got %v", affected)
	}
	rec, _ := s.Get("cid1")
	if rec.Status != state.ContainerStatusRemoved {
		t.Errorf("status should remain Removed, got %q", rec.Status)
	}
}

// ── UpdatedAt ─────────────────────────────────────────────────────────────────

func TestContainerUpdatedAt_ChangesOnTransition(t *testing.T) {
	s := newContainerStore(t)
	rec := s.Register(cparams("cid1", "web", "nginx:latest", state.ContainerOriginInBand))
	before := rec.UpdatedAt

	time.Sleep(2 * time.Millisecond)
	_ = s.MarkRemoved("cid1")

	after, _ := s.Get("cid1")
	if !after.UpdatedAt.After(before) {
		t.Errorf("UpdatedAt should advance on transition: before=%v after=%v",
			before, after.UpdatedAt)
	}
}

// ── Concurrency ───────────────────────────────────────────────────────────────

func TestContainerStore_ConcurrentRegistersAndReads(t *testing.T) {
	s := newContainerStore(t)
	const n = 200

	var wg sync.WaitGroup
	for i := range n {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Register(cparams(
				fmt.Sprintf("container-id-%06d", i),
				fmt.Sprintf("web-%d", i),
				"nginx:latest",
				state.ContainerOriginInBand,
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
			_ = s.MarkRemoved(fmt.Sprintf("container-id-%06d", i))
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

	removed := s.ByStatus(state.ContainerStatusRemoved)
	if len(removed) != n/2 {
		t.Errorf("removed: got %d, want %d", len(removed), n/2)
	}
}
