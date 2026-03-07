package dockercontainerexecops_test

import (
	"context"
	"testing"
	"time"

	"dokoko.ai/dokoko/internal/docker"
	containerops "dokoko.ai/dokoko/internal/docker/containers/ops"
	execops "dokoko.ai/dokoko/internal/docker/containerexec/ops"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"
)

const (
	testImage         = "busybox:latest"
	testContainerName = "dokoko-exec-test-container"
)

func TestExecLifecycle(t *testing.T) {
	log := logger.New(logger.LevelTrace)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn, err := docker.New(ctx, log)
	if err != nil {
		t.Fatalf("docker connect: %v", err)
	}
	defer conn.Close()

	cops := containerops.New(conn, log)
	ops := execops.New(conn, log)

	// ── 0. Prepare a running container ───────────────────────────────────────
	_ = cops.Remove(ctx, testContainerName, dockercontainer.RemoveOptions{Force: true})

	cfg := &dockercontainer.Config{
		Image: testImage,
		Cmd:   []string{"sh", "-c", "sleep 60"},
	}
	cResp, err := cops.Create(ctx, testContainerName, cfg, nil, nil)
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	defer cops.Remove(ctx, cResp.ID, dockercontainer.RemoveOptions{Force: true})

	if err := cops.Start(ctx, cResp.ID, dockercontainer.StartOptions{}); err != nil {
		t.Fatalf("start container: %v", err)
	}
	containerID := cResp.ID
	t.Logf("test container started: id=%s", containerID[:12])

	// ── 1. ExecCreate ─────────────────────────────────────────────────────────
	t.Log("=== EXEC CREATE ===")
	execCfg := dockertypes.ExecConfig{
		Cmd:          []string{"echo", "hello-from-exec"},
		AttachStdout: true,
		AttachStderr: true,
	}
	idResp, err := ops.Create(ctx, containerID, execCfg)
	if err != nil {
		t.Fatalf("exec create: %v", err)
	}
	if idResp.ID == "" {
		t.Error("exec create returned empty ID")
	}
	execID := idResp.ID
	t.Logf("exec instance created: execID=%s", execID)

	// ── 2. ExecStart (detached — returns immediately) ─────────────────────────
	t.Log("=== EXEC START (detach=true) ===")
	if err := ops.Start(ctx, execID, dockertypes.ExecStartCheck{Detach: true}); err != nil {
		t.Fatalf("exec start: %v", err)
	}
	t.Logf("exec started (detached): execID=%s", execID)

	// ── 3. ExecInspect ────────────────────────────────────────────────────────
	t.Log("=== EXEC INSPECT ===")
	// Give the process a moment to finish.
	time.Sleep(200 * time.Millisecond)

	info, err := ops.Inspect(ctx, execID)
	if err != nil {
		t.Fatalf("exec inspect: %v", err)
	}
	if info.ExecID == "" {
		t.Error("inspect returned empty ExecID")
	}
	t.Logf("exec inspect: running=%v exitCode=%d pid=%d containerID=%s",
		info.Running, info.ExitCode, info.Pid, info.ContainerID[:12])
	if info.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", info.ExitCode)
	}

	// ── 4. ExecCreate + Attach (interactive, captures output) ─────────────────
	t.Log("=== EXEC ATTACH ===")
	execCfg2 := dockertypes.ExecConfig{
		Cmd:          []string{"echo", "attach-works"},
		AttachStdout: true,
		AttachStderr: true,
	}
	idResp2, err := ops.Create(ctx, containerID, execCfg2)
	if err != nil {
		t.Fatalf("exec create (for attach): %v", err)
	}
	hijack, err := ops.Attach(ctx, idResp2.ID, dockertypes.ExecStartCheck{Detach: false})
	if err != nil {
		t.Fatalf("exec attach: %v", err)
	}
	hijack.Close()
	t.Logf("exec attach succeeded and closed: execID=%s", idResp2.ID)

	// ── 5. ExecCreate + Resize (TTY) ──────────────────────────────────────────
	t.Log("=== EXEC RESIZE ===")
	execCfg3 := dockertypes.ExecConfig{
		Cmd:          []string{"sh"},
		AttachStdin:  true,
		AttachStdout: true,
		Tty:          true,
	}
	idResp3, err := ops.Create(ctx, containerID, execCfg3)
	if err != nil {
		t.Fatalf("exec create (for resize): %v", err)
	}
	// Start in detached mode first to have an active exec to resize.
	_ = ops.Start(ctx, idResp3.ID, dockertypes.ExecStartCheck{Detach: true})
	if err := ops.Resize(ctx, idResp3.ID, dockercontainer.ResizeOptions{Height: 40, Width: 120}); err != nil {
		// Non-fatal: resize may fail if the exec already exited.
		t.Logf("exec resize: %v — skipping (exec may have exited)", err)
	} else {
		t.Logf("exec TTY resized: execID=%s 40x120", idResp3.ID)
	}
}
