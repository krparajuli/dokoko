package dockerbuildops_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dokoko.ai/dokoko/internal/docker"
	dockerbuildops "dokoko.ai/dokoko/internal/docker/builds/ops"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
	dockerimage "github.com/docker/docker/api/types/image"
)

const testImageTag = "dokoko-build-test:latest"

func setup(t *testing.T) (*dockerbuildops.Ops, func()) {
	t.Helper()

	log := logger.New(logger.LevelTrace)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	conn, err := docker.New(ctx, log)
	if err != nil {
		cancel()
		t.Fatalf("docker connect: %v", err)
	}

	ops := dockerbuildops.New(conn, log)

	cleanup := func() {
		cancel()
		// Remove the test image if it was created.
		_, _ = conn.Client().ImageRemove(
			context.Background(), testImageTag,
			dockertypes.ImageRemoveOptions{Force: true, PruneChildren: true},
		)
		if err := conn.Close(); err != nil {
			t.Logf("warn: conn close: %v", err)
		}
	}

	return ops, cleanup
}

// writeTempDockerfile writes a minimal Dockerfile to a new temp directory and
// returns the directory path.
func writeTempDockerfile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(content), 0o644); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}
	return dir
}

// ── Build from local directory ────────────────────────────────────────────────

func TestBuild_FromContextDir(t *testing.T) {
	ops, cleanup := setup(t)
	defer cleanup()

	// A minimal Dockerfile: inherits busybox and sets a label.
	// busybox is a tiny image that is often already cached.
	dockerfile := "FROM busybox:latest\nLABEL dokoko.test=build-from-dir\n"
	dir := writeTempDockerfile(t, dockerfile)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	t.Log("=== BUILD FROM DIR ===")
	resp, err := ops.Build(ctx, dockerbuildops.BuildRequest{
		ContextDir: dir,
		Tags:       []string{testImageTag},
		NoCache:    true,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	t.Logf("imageID=%s lines=%d", resp.ImageID, len(resp.Log))
	if resp.ImageID == "" {
		t.Error("expected non-empty ImageID")
	}
	if len(resp.Log) == 0 {
		t.Error("expected at least one log line")
	}

	// Verify the image exists in the local store.
	info, _, err := conn(t).ImageInspectWithRaw(context.Background(), testImageTag)
	if err != nil {
		t.Fatalf("verify image exists: %v", err)
	}
	t.Logf("verified: id=%s tags=%v", info.ID, info.RepoTags)
}

// ── Build with build arguments ────────────────────────────────────────────────

func TestBuild_WithBuildArgs(t *testing.T) {
	ops, cleanup := setup(t)
	defer cleanup()

	dockerfile := "FROM busybox:latest\nARG BUILD_VERSION=unknown\nLABEL version=${BUILD_VERSION}\n"
	dir := writeTempDockerfile(t, dockerfile)

	val := "1.2.3"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	t.Log("=== BUILD WITH BUILD ARGS ===")
	resp, err := ops.Build(ctx, dockerbuildops.BuildRequest{
		ContextDir: dir,
		Tags:       []string{testImageTag},
		BuildArgs:  map[string]*string{"BUILD_VERSION": &val},
		NoCache:    true,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	t.Logf("imageID=%s lines=%d", resp.ImageID, len(resp.Log))
	if resp.ImageID == "" {
		t.Error("expected non-empty ImageID")
	}
}

// ── Multi-stage build with Target ─────────────────────────────────────────────

func TestBuild_MultiStage_WithTarget(t *testing.T) {
	ops, cleanup := setup(t)
	defer cleanup()

	dockerfile := `FROM busybox:latest AS base
LABEL stage=base
FROM busybox:latest AS final
LABEL stage=final
`
	dir := writeTempDockerfile(t, dockerfile)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	t.Log("=== MULTI-STAGE BUILD (target=base) ===")
	resp, err := ops.Build(ctx, dockerbuildops.BuildRequest{
		ContextDir: dir,
		Tags:       []string{testImageTag},
		Target:     "base",
		NoCache:    true,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	t.Logf("imageID=%s lines=%d", resp.ImageID, len(resp.Log))
	if resp.ImageID == "" {
		t.Error("expected non-empty ImageID from multi-stage build targeting 'base'")
	}
}

// ── Dockerfile parse error surfaces as error ──────────────────────────────────

func TestBuild_InvalidDockerfile_ReturnsError(t *testing.T) {
	ops, cleanup := setup(t)
	defer cleanup()

	// "NOTACOMMAND" is not a valid Dockerfile directive.
	dockerfile := "NOTACOMMAND\n"
	dir := writeTempDockerfile(t, dockerfile)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	t.Log("=== BUILD WITH INVALID DOCKERFILE ===")
	_, err := ops.Build(ctx, dockerbuildops.BuildRequest{
		ContextDir: dir,
	})
	if err == nil {
		t.Error("expected error for invalid Dockerfile, got nil")
	} else {
		t.Logf("got expected error: %v", err)
	}
}

// ── Validation ────────────────────────────────────────────────────────────────

func TestBuild_MissingContext_ReturnsError(t *testing.T) {
	ops, cleanup := setup(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := ops.Build(ctx, dockerbuildops.BuildRequest{
		// No ContextDir, ContextTar, or RemoteContext.
	})
	if err == nil {
		t.Error("expected validation error when no context is provided")
	} else {
		t.Logf("got expected error: %v", err)
	}
}

func TestBuild_BothContextSources_ReturnsError(t *testing.T) {
	ops, cleanup := setup(t)
	defer cleanup()

	dir := writeTempDockerfile(t, "FROM busybox:latest\n")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := ops.Build(ctx, dockerbuildops.BuildRequest{
		ContextDir:    dir,
		RemoteContext: "https://github.com/example/repo.git",
	})
	if err == nil {
		t.Error("expected error when both ContextDir and RemoteContext are set")
	} else {
		t.Logf("got expected error: %v", err)
	}
}

// ── conn helper for inspect ───────────────────────────────────────────────────

// conn returns a short-lived Docker client for inspect/cleanup calls inside tests.
// It panics on error (test context only).
func conn(t *testing.T) interface {
	ImageInspectWithRaw(ctx context.Context, image string) (dockertypes.ImageInspect, []byte, error)
	ImageRemove(ctx context.Context, image string, options dockertypes.ImageRemoveOptions) ([]dockerimage.DeleteResponse, error)
} {
	t.Helper()
	log := logger.New(logger.LevelSilent)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := docker.New(ctx, log)
	if err != nil {
		t.Fatalf("conn helper: docker connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c.Client()
}
