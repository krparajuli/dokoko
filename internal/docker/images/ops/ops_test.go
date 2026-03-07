package dockerimageops_test

import (
	"context"
	"testing"
	"time"

	"dokoko.ai/dokoko/internal/docker"
	dockerimageops "dokoko.ai/dokoko/internal/docker/images/ops"
	"dokoko.ai/dokoko/pkg/logger"
	dockerimage "github.com/docker/docker/api/types/image"
)

const testImage = "busybox:latest"

func setup(t *testing.T) (*dockerimageops.Ops, func()) {
	t.Helper()

	log := logger.New(logger.LevelTrace)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	conn, err := docker.New(ctx, log)
	if err != nil {
		cancel()
		t.Fatalf("docker connect: %v", err)
	}

	ops := dockerimageops.New(conn, log)

	cleanup := func() {
		cancel()
		if err := conn.Close(); err != nil {
			t.Logf("warn: conn close: %v", err)
		}
	}

	return ops, cleanup
}

func TestBusyboxLifecycle(t *testing.T) {
	ops, cleanup := setup(t)
	defer cleanup()

	ctx := context.Background()

	// ── 1. Pull ──────────────────────────────────────────────────────────────
	t.Log("=== PULL ===")
	pullCtx, cancelPull := context.WithTimeout(ctx, 2*time.Minute)
	defer cancelPull()

	if err := ops.Pull(pullCtx, testImage, dockerimage.PullOptions{}); err != nil {
		t.Fatalf("pull %s: %v", testImage, err)
	}

	// ── 2. List (should contain busybox) ─────────────────────────────────────
	t.Log("=== LIST (after pull) ===")
	images, err := ops.List(ctx, dockerimage.ListOptions{})
	if err != nil {
		t.Fatalf("list after pull: %v", err)
	}

	found := containsTag(images, testImage)
	if !found {
		t.Errorf("expected %q in image list after pull, not found (got %d images)", testImage, len(images))
	} else {
		t.Logf("confirmed: %s present in local store (%d total images)", testImage, len(images))
	}

	// ── 3. Inspect ───────────────────────────────────────────────────────────
	t.Log("=== INSPECT ===")
	info, err := ops.Inspect(ctx, testImage)
	if err != nil {
		t.Fatalf("inspect %s: %v", testImage, err)
	}

	if info.ID == "" {
		t.Error("inspect returned empty ID")
	}
	t.Logf("inspect: id=%s os=%s arch=%s size=%d tags=%v",
		info.ID[:12], info.Os, info.Architecture, info.Size, info.RepoTags)

	// ── 4. Remove ────────────────────────────────────────────────────────────
	t.Log("=== REMOVE ===")
	deleted, err := ops.Remove(ctx, testImage, dockerimage.RemoveOptions{Force: true, PruneChildren: true})
	if err != nil {
		t.Fatalf("remove %s: %v", testImage, err)
	}
	t.Logf("remove: %d response entries", len(deleted))
	for _, r := range deleted {
		if r.Untagged != "" {
			t.Logf("  untagged: %s", r.Untagged)
		}
		if r.Deleted != "" {
			t.Logf("  deleted:  %s", r.Deleted)
		}
	}

	// ── 5. List (busybox should be gone) ─────────────────────────────────────
	t.Log("=== LIST (after remove) ===")
	images, err = ops.List(ctx, dockerimage.ListOptions{})
	if err != nil {
		t.Fatalf("list after remove: %v", err)
	}

	if containsTag(images, testImage) {
		t.Errorf("%q still present in image list after remove", testImage)
	} else {
		t.Logf("confirmed: %s absent from local store (%d total images)", testImage, len(images))
	}
}

// containsTag returns true if any image summary carries the given tag.
func containsTag(images []dockerimage.Summary, tag string) bool {
	for _, img := range images {
		for _, t := range img.RepoTags {
			if t == tag {
				return true
			}
		}
	}
	return false
}
