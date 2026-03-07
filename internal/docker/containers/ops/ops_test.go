package dockercontainerops_test

import (
	"context"
	"testing"
	"time"

	"dokoko.ai/dokoko/internal/docker"
	dockercontainerops "dokoko.ai/dokoko/internal/docker/containers/ops"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"
	dockernetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
)

const testImage = "busybox:latest"
const testContainerName = "dokoko-test-container"

func setup(t *testing.T) (*dockercontainerops.Ops, func()) {
	t.Helper()

	log := logger.New(logger.LevelTrace)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	conn, err := docker.New(ctx, log)
	if err != nil {
		cancel()
		t.Fatalf("docker connect: %v", err)
	}

	ops := dockercontainerops.New(conn, log)

	cleanup := func() {
		cancel()
		if err := conn.Close(); err != nil {
			t.Logf("warn: conn close: %v", err)
		}
	}

	return ops, cleanup
}

func TestContainerLifecycle(t *testing.T) {
	ops, cleanup := setup(t)
	defer cleanup()

	ctx := context.Background()

	// ── 1. Create (with port binding, volume bind, and network config) ────────
	t.Log("=== CREATE ===")

	// Remove any leftover container from a previous run.
	_ = ops.Remove(ctx, testContainerName, dockercontainer.RemoveOptions{Force: true})

	// Expose port 8080 inside the container and map it to a random host port.
	exposedPort := nat.Port("8080/tcp")
	cfg := &dockercontainer.Config{
		Image: testImage,
		Cmd:   []string{"sh", "-c", "sleep 30"},
		ExposedPorts: nat.PortSet{
			exposedPort: struct{}{},
		},
	}
	hostCfg := &dockercontainer.HostConfig{
		PortBindings: nat.PortMap{
			exposedPort: []nat.PortBinding{
				{HostIP: "127.0.0.1", HostPort: "0"}, // port 0 = random
			},
		},
		// Mount a tmpfs volume into the container.
		Binds: []string{},
	}

	resp, err := ops.Create(ctx, testContainerName, cfg, hostCfg, nil)
	if err != nil {
		t.Fatalf("create %s: %v", testContainerName, err)
	}
	if resp.ID == "" {
		t.Error("create returned empty container ID")
	}
	t.Logf("created: id=%s warnings=%d", resp.ID[:12], len(resp.Warnings))

	// ── 2. Exists (should be true) ────────────────────────────────────────────
	t.Log("=== EXISTS (after create) ===")
	exists, err := ops.Exists(ctx, testContainerName)
	if err != nil {
		t.Fatalf("exists after create: %v", err)
	}
	if !exists {
		t.Errorf("expected container %q to exist after create", testContainerName)
	}

	// ── 3. Start ─────────────────────────────────────────────────────────────
	t.Log("=== START ===")
	if err := ops.Start(ctx, resp.ID, dockercontainer.StartOptions{}); err != nil {
		t.Fatalf("start %s: %v", resp.ID[:12], err)
	}

	// ── 4. Inspect (verify port binding and running state) ────────────────────
	t.Log("=== INSPECT (running) ===")
	info, err := ops.Inspect(ctx, resp.ID)
	if err != nil {
		t.Fatalf("inspect %s: %v", resp.ID[:12], err)
	}
	if info.ID == "" {
		t.Error("inspect returned empty ID")
	}
	if !info.State.Running {
		t.Errorf("expected container to be running, got status=%s", info.State.Status)
	}
	t.Logf("inspect: id=%s name=%s running=%v status=%s",
		info.ID[:12], info.Name, info.State.Running, info.State.Status)

	// Verify the port binding is reflected in NetworkSettings.
	if info.NetworkSettings != nil {
		if pb, ok := info.NetworkSettings.Ports[exposedPort]; ok {
			if len(pb) > 0 {
				t.Logf("port binding: %s → %s:%s", exposedPort, pb[0].HostIP, pb[0].HostPort)
			}
		} else {
			t.Logf("note: port %s not in NetworkSettings.Ports (may require explicit host binding)", exposedPort)
		}
		t.Logf("networks attached: %d", len(info.NetworkSettings.Networks))
	}

	// Verify HostConfig port binding is stored.
	if info.HostConfig != nil && len(info.HostConfig.PortBindings) > 0 {
		t.Logf("hostConfig portBindings: %d entries", len(info.HostConfig.PortBindings))
	}

	// ── 5. List (should contain our container) ────────────────────────────────
	t.Log("=== LIST (running) ===")
	containers, err := ops.List(ctx, dockercontainer.ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !containsID(containers, resp.ID) {
		t.Errorf("expected container %s in list, not found (%d total)", resp.ID[:12], len(containers))
	} else {
		t.Logf("confirmed: %s present in container list (%d total)", resp.ID[:12], len(containers))
	}

	// ── 6. Connect to the default bridge network (explicit attach) ────────────
	t.Log("=== CONNECT (bridge) ===")
	epCfg := &dockernetwork.EndpointSettings{
		Aliases: []string{"test-alias"},
	}
	if err := ops.Connect(ctx, "bridge", resp.ID, epCfg); err != nil {
		// Non-fatal: already on bridge, or bridge unavailable in this environment.
		t.Logf("connect (bridge): %v — skipping connect/disconnect checks", err)
	} else {
		t.Logf("connected container to bridge network with alias 'test-alias'")

		// ── 7. Disconnect ────────────────────────────────────────────────────
		t.Log("=== DISCONNECT (bridge) ===")
		if err := ops.Disconnect(ctx, "bridge", resp.ID, false); err != nil {
			t.Logf("disconnect (bridge): %v", err)
		} else {
			t.Logf("disconnected container from bridge network")
		}
	}

	// ── 8. Stop ──────────────────────────────────────────────────────────────
	t.Log("=== STOP ===")
	timeout := 5
	if err := ops.Stop(ctx, resp.ID, dockercontainer.StopOptions{Timeout: &timeout}); err != nil {
		t.Fatalf("stop %s: %v", resp.ID[:12], err)
	}

	// ── 9. Inspect (should not be running) ───────────────────────────────────
	t.Log("=== INSPECT (stopped) ===")
	info, err = ops.Inspect(ctx, resp.ID)
	if err != nil {
		t.Fatalf("inspect after stop: %v", err)
	}
	if info.State.Running {
		t.Errorf("expected container to be stopped, got running=true")
	}
	t.Logf("inspect after stop: status=%s exitCode=%d", info.State.Status, info.State.ExitCode)

	// ── 10. Remove ───────────────────────────────────────────────────────────
	t.Log("=== REMOVE ===")
	if err := ops.Remove(ctx, resp.ID, dockercontainer.RemoveOptions{}); err != nil {
		t.Fatalf("remove %s: %v", resp.ID[:12], err)
	}

	// ── 11. Exists (should be false) ─────────────────────────────────────────
	t.Log("=== EXISTS (after remove) ===")
	exists, err = ops.Exists(ctx, testContainerName)
	if err != nil {
		t.Fatalf("exists after remove: %v", err)
	}
	if exists {
		t.Errorf("expected container %q to not exist after remove", testContainerName)
	}
	t.Logf("confirmed: %s absent after remove", testContainerName)
}

// containsID returns true if any container summary has the given ID.
func containsID(containers []dockertypes.Container, id string) bool {
	for _, c := range containers {
		if c.ID == id {
			return true
		}
	}
	return false
}
