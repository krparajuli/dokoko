package dockernetworkops_test

import (
	"context"
	"testing"
	"time"

	"dokoko.ai/dokoko/internal/docker"
	dockernetworkops "dokoko.ai/dokoko/internal/docker/networks/ops"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
	dockerfilters "github.com/docker/docker/api/types/filters"
)

const testNetwork = "dokoko-test-net"

func setup(t *testing.T) (*dockernetworkops.Ops, func()) {
	t.Helper()

	log := logger.New(logger.LevelTrace)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	conn, err := docker.New(ctx, log)
	if err != nil {
		cancel()
		t.Fatalf("docker connect: %v", err)
	}

	ops := dockernetworkops.New(conn, log)

	cleanup := func() {
		cancel()
		if err := conn.Close(); err != nil {
			t.Logf("warn: conn close: %v", err)
		}
	}

	return ops, cleanup
}

func TestNetworkLifecycle(t *testing.T) {
	ops, cleanup := setup(t)
	defer cleanup()

	ctx := context.Background()

	// ── 1. Create ─────────────────────────────────────────────────────────────
	t.Log("=== CREATE ===")
	resp, err := ops.Create(ctx, testNetwork, dockertypes.NetworkCreate{
		Driver: "bridge",
	})
	if err != nil {
		t.Fatalf("create %s: %v", testNetwork, err)
	}
	if resp.ID == "" {
		t.Error("expected non-empty network ID")
	}
	t.Logf("created: name=%s id=%s warning=%q", testNetwork, resp.ID, resp.Warning)

	// Ensure cleanup even on test failure.
	t.Cleanup(func() {
		_ = ops.Remove(context.Background(), resp.ID)
	})

	// ── 2. List (should contain our network) ──────────────────────────────────
	t.Log("=== LIST (after create) ===")
	networks, err := ops.List(ctx, dockertypes.NetworkListOptions{})
	if err != nil {
		t.Fatalf("list after create: %v", err)
	}

	if !containsNetwork(networks, testNetwork) {
		t.Errorf("expected %q in network list, not found (%d total)", testNetwork, len(networks))
	} else {
		t.Logf("confirmed: %s present in local store (%d total)", testNetwork, len(networks))
	}

	// ── 3. Inspect ────────────────────────────────────────────────────────────
	t.Log("=== INSPECT ===")
	info, err := ops.Inspect(ctx, resp.ID, dockertypes.NetworkInspectOptions{})
	if err != nil {
		t.Fatalf("inspect %s: %v", resp.ID, err)
	}
	if info.Name != testNetwork {
		t.Errorf("inspect name: got %q, want %q", info.Name, testNetwork)
	}
	t.Logf("inspect: id=%s name=%s driver=%s scope=%s",
		info.ID, info.Name, info.Driver, info.Scope)

	// ── 4. Remove ─────────────────────────────────────────────────────────────
	t.Log("=== REMOVE ===")
	if err := ops.Remove(ctx, resp.ID); err != nil {
		t.Fatalf("remove %s: %v", resp.ID, err)
	}
	t.Logf("removed: %s", resp.ID)

	// ── 5. List (network should be gone) ──────────────────────────────────────
	t.Log("=== LIST (after remove) ===")
	networks, err = ops.List(ctx, dockertypes.NetworkListOptions{})
	if err != nil {
		t.Fatalf("list after remove: %v", err)
	}

	if containsNetwork(networks, testNetwork) {
		t.Errorf("%q still present after remove", testNetwork)
	} else {
		t.Logf("confirmed: %s absent from local store (%d total)", testNetwork, len(networks))
	}
}

func TestNetworkPrune(t *testing.T) {
	ops, cleanup := setup(t)
	defer cleanup()

	ctx := context.Background()

	// Create a network to prune.
	resp, err := ops.Create(ctx, testNetwork+"-prune", dockertypes.NetworkCreate{Driver: "bridge"})
	if err != nil {
		t.Fatalf("create network for prune: %v", err)
	}
	t.Logf("created network: id=%s", resp.ID)

	// Safety net: remove manually if prune doesn't catch it.
	t.Cleanup(func() { _ = ops.Remove(context.Background(), resp.ID) })

	report, err := ops.Prune(ctx, dockerfilters.NewArgs())
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	t.Logf("prune report: deleted=%v", report.NetworksDeleted)
}

// containsNetwork returns true if any network in the list has the given name.
func containsNetwork(networks []dockertypes.NetworkResource, name string) bool {
	for _, n := range networks {
		if n.Name == name {
			return true
		}
	}
	return false
}
