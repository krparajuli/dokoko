package dockervolumeops_test

import (
	"context"
	"testing"
	"time"

	"dokoko.ai/dokoko/internal/docker"
	dockervolumeops "dokoko.ai/dokoko/internal/docker/volumes/ops"
	"dokoko.ai/dokoko/pkg/logger"
	dockerfilters "github.com/docker/docker/api/types/filters"
	dockervolume "github.com/docker/docker/api/types/volume"
)

const testVolume = "dokoko-test-vol"

func setup(t *testing.T) (*dockervolumeops.Ops, func()) {
	t.Helper()

	log := logger.New(logger.LevelTrace)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	conn, err := docker.New(ctx, log)
	if err != nil {
		cancel()
		t.Fatalf("docker connect: %v", err)
	}

	ops := dockervolumeops.New(conn, log)

	cleanup := func() {
		cancel()
		if err := conn.Close(); err != nil {
			t.Logf("warn: conn close: %v", err)
		}
	}

	return ops, cleanup
}

func TestVolumeLifecycle(t *testing.T) {
	ops, cleanup := setup(t)
	defer cleanup()

	ctx := context.Background()

	// ── 1. Create ─────────────────────────────────────────────────────────────
	t.Log("=== CREATE ===")
	vol, err := ops.Create(ctx, dockervolume.CreateOptions{
		Name:   testVolume,
		Driver: "local",
	})
	if err != nil {
		t.Fatalf("create %s: %v", testVolume, err)
	}
	if vol.Name != testVolume {
		t.Errorf("name: got %q, want %q", vol.Name, testVolume)
	}
	t.Logf("created: name=%s driver=%s mountpoint=%s", vol.Name, vol.Driver, vol.Mountpoint)

	// Ensure cleanup even on test failure.
	t.Cleanup(func() {
		_ = ops.Remove(context.Background(), testVolume, true)
	})

	// ── 2. List (should contain our volume) ───────────────────────────────────
	t.Log("=== LIST (after create) ===")
	resp, err := ops.List(ctx, dockervolume.ListOptions{})
	if err != nil {
		t.Fatalf("list after create: %v", err)
	}

	if !containsVolume(resp.Volumes, testVolume) {
		t.Errorf("expected %q in volume list, not found (%d total)", testVolume, len(resp.Volumes))
	} else {
		t.Logf("confirmed: %s present in local store (%d total)", testVolume, len(resp.Volumes))
	}

	// ── 3. Inspect ────────────────────────────────────────────────────────────
	t.Log("=== INSPECT ===")
	info, err := ops.Inspect(ctx, testVolume)
	if err != nil {
		t.Fatalf("inspect %s: %v", testVolume, err)
	}
	if info.Name != testVolume {
		t.Errorf("inspect name: got %q, want %q", info.Name, testVolume)
	}
	t.Logf("inspect: name=%s driver=%s scope=%s mountpoint=%s",
		info.Name, info.Driver, info.Scope, info.Mountpoint)

	// ── 4. Remove ─────────────────────────────────────────────────────────────
	t.Log("=== REMOVE ===")
	if err := ops.Remove(ctx, testVolume, false); err != nil {
		t.Fatalf("remove %s: %v", testVolume, err)
	}
	t.Logf("removed: %s", testVolume)

	// ── 5. List (volume should be gone) ───────────────────────────────────────
	t.Log("=== LIST (after remove) ===")
	resp, err = ops.List(ctx, dockervolume.ListOptions{})
	if err != nil {
		t.Fatalf("list after remove: %v", err)
	}

	if containsVolume(resp.Volumes, testVolume) {
		t.Errorf("%q still present after remove", testVolume)
	} else {
		t.Logf("confirmed: %s absent from local store (%d total)", testVolume, len(resp.Volumes))
	}
}

func TestVolumePrune(t *testing.T) {
	ops, cleanup := setup(t)
	defer cleanup()

	ctx := context.Background()

	// Create an anonymous volume (no name → Docker generates a UUID-like name).
	// Since Docker 23+, VolumesPrune with an empty filter only removes anonymous
	// volumes; named volumes require dangling=false.  Creating without a name
	// makes the volume anonymous and thus prunable by default.
	vol, err := ops.Create(ctx, dockervolume.CreateOptions{Driver: "local"})
	if err != nil {
		t.Fatalf("create anonymous vol: %v", err)
	}
	t.Logf("created anonymous volume: %s", vol.Name)

	// Safety net: remove it manually if prune doesn't catch it (e.g. another
	// test leaked a container reference to it).
	t.Cleanup(func() { _ = ops.Remove(context.Background(), vol.Name, true) })

	report, err := ops.Prune(ctx, dockerfilters.NewArgs())
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	t.Logf("prune report: deleted=%v spaceReclaimed=%d", report.VolumesDeleted, report.SpaceReclaimed)

	found := false
	for _, v := range report.VolumesDeleted {
		if v == vol.Name {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected anonymous volume %q in VolumesDeleted, got %v", vol.Name, report.VolumesDeleted)
	}
}

// containsVolume returns true if any volume in the list has the given name.
func containsVolume(volumes []*dockervolume.Volume, name string) bool {
	for _, v := range volumes {
		if v.Name == name {
			return true
		}
	}
	return false
}
