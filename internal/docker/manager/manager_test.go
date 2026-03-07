package dockermanager_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	dockermanager "dokoko.ai/dokoko/internal/docker/manager"
	dockerimagestate "dokoko.ai/dokoko/internal/docker/images/state"
	"dokoko.ai/dokoko/pkg/logger"
	dockerclient "github.com/docker/docker/client"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func silentLogger() *logger.Logger { return logger.New(logger.LevelSilent) }

// requireDocker skips the test when the Docker daemon is not reachable.
func requireDocker(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("Docker client unavailable: %v", err)
	}
	defer cli.Close()
	if _, err := cli.Ping(ctx); err != nil {
		t.Skipf("Docker daemon not reachable: %v", err)
	}
}

// newManager creates a Manager and registers Close as a test cleanup.
func newManager(t *testing.T) *dockermanager.Manager {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mgr, err := dockermanager.New(ctx, silentLogger())
	if err != nil {
		t.Fatalf("dockermanager.New: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	return mgr
}

// ctx10s returns a context with a 10-second deadline, suited for reconnect tests.
func ctx10s(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// ── New ───────────────────────────────────────────────────────────────────────

func TestNew_AllSubsystemsNonNil(t *testing.T) {
	requireDocker(t)
	mgr := newManager(t)

	if mgr.Images() == nil {
		t.Error("Images() is nil after New")
	}
	if mgr.Builds() == nil {
		t.Error("Builds() is nil after New")
	}
	if mgr.Networks() == nil {
		t.Error("Networks() is nil after New")
	}
	if mgr.Volumes() == nil {
		t.Error("Volumes() is nil after New")
	}
	if mgr.Containers() == nil {
		t.Error("Containers() is nil after New")
	}
	if mgr.Exec() == nil {
		t.Error("Exec() is nil after New")
	}
}

func TestNew_AllStateAccessorsNonNil(t *testing.T) {
	requireDocker(t)
	mgr := newManager(t)

	if mgr.ImageState() == nil {
		t.Error("ImageState() is nil")
	}
	if mgr.ContainerState() == nil {
		t.Error("ContainerState() is nil")
	}
	if mgr.BuildState() == nil {
		t.Error("BuildState() is nil")
	}
	if mgr.ExecState() == nil {
		t.Error("ExecState() is nil")
	}
	if mgr.NetworkState() == nil {
		t.Error("NetworkState() is nil")
	}
	if mgr.VolumeState() == nil {
		t.Error("VolumeState() is nil")
	}
}

func TestNew_StoresAccessibleAfterNew(t *testing.T) {
	requireDocker(t)
	mgr := newManager(t)

	// Stores are bootstrapped during connect; each must be non-nil and
	// queryable without panicking.
	if mgr.Images().Store() == nil {
		t.Fatal("Images().Store() is nil")
	}
	_ = mgr.Images().Store().All()

	if mgr.Networks().Store() == nil {
		t.Fatal("Networks().Store() is nil")
	}

	if mgr.Volumes().Store() == nil {
		t.Fatal("Volumes().Store() is nil")
	}

	if mgr.Builds().BuildStore() == nil {
		t.Fatal("Builds().BuildStore() is nil")
	}
}

// TestNew_ReturnsErrorWhenDockerUnreachable verifies that New propagates a
// connection failure.  It does not require a running daemon — it intentionally
// uses an unreachable address.
func TestNew_ReturnsErrorWhenDockerUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := dockermanager.New(ctx, silentLogger(),
		dockerclient.WithHost("tcp://127.0.0.1:19998"), // nothing listening here
	)
	if err == nil {
		t.Error("expected error when Docker is unreachable, got nil")
	}
}

// ── Ping ──────────────────────────────────────────────────────────────────────

func TestPing_SucceedsAfterNew(t *testing.T) {
	requireDocker(t)
	mgr := newManager(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := mgr.Ping(ctx); err != nil {
		t.Errorf("Ping after New: %v", err)
	}
}

func TestPing_ReturnsErrorAfterClose(t *testing.T) {
	requireDocker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mgr, err := dockermanager.New(ctx, silentLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := mgr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := mgr.Ping(context.Background()); err == nil {
		t.Error("expected error from Ping after Close, got nil")
	}
}

// ── Refresh ───────────────────────────────────────────────────────────────────

func TestRefresh_SucceedsRepeatedly(t *testing.T) {
	requireDocker(t)
	mgr := newManager(t)
	ctx := ctx10s(t)

	for range 3 {
		if err := mgr.Refresh(ctx); err != nil {
			t.Fatalf("Refresh: %v", err)
		}
	}
}

func TestRefresh_StoreGrowsOrStaysStableOnRepeat(t *testing.T) {
	requireDocker(t)
	mgr := newManager(t)
	ctx := ctx10s(t)

	sizeBefore := mgr.Images().Store().Size()
	if err := mgr.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	sizeAfter := mgr.Images().Store().Size()

	// A repeated Refresh on unchanged Docker state should not shrink the store
	// (records move to deleted_out_of_band rather than disappearing).
	if sizeAfter < sizeBefore {
		t.Errorf("store shrank after Refresh: before=%d after=%d", sizeBefore, sizeAfter)
	}
}

func TestRefresh_ReturnsErrorAfterClose(t *testing.T) {
	requireDocker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mgr, err := dockermanager.New(ctx, silentLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := mgr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := mgr.Refresh(context.Background()); err == nil {
		t.Error("expected error from Refresh after Close")
	}
}

// ── Reconnect ─────────────────────────────────────────────────────────────────

func TestReconnect_AllSubsystemsAccessibleAfterReconnect(t *testing.T) {
	requireDocker(t)
	mgr := newManager(t)
	ctx := ctx10s(t)

	if err := mgr.Reconnect(ctx); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}

	if mgr.Images() == nil {
		t.Error("Images() nil after Reconnect")
	}
	if mgr.Builds() == nil {
		t.Error("Builds() nil after Reconnect")
	}
	if mgr.Networks() == nil {
		t.Error("Networks() nil after Reconnect")
	}
	if mgr.Volumes() == nil {
		t.Error("Volumes() nil after Reconnect")
	}
	if mgr.Containers() == nil {
		t.Error("Containers() nil after Reconnect")
	}
	if mgr.Exec() == nil {
		t.Error("Exec() nil after Reconnect")
	}
}

func TestReconnect_StateInstancesPreserved(t *testing.T) {
	requireDocker(t)
	mgr := newManager(t)

	imgBefore     := mgr.ImageState()
	contBefore    := mgr.ContainerState()
	buildBefore   := mgr.BuildState()
	execBefore    := mgr.ExecState()
	netBefore     := mgr.NetworkState()
	volBefore     := mgr.VolumeState()

	if err := mgr.Reconnect(ctx10s(t)); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}

	if mgr.ImageState() != imgBefore {
		t.Error("ImageState instance changed after Reconnect")
	}
	if mgr.ContainerState() != contBefore {
		t.Error("ContainerState instance changed after Reconnect")
	}
	if mgr.BuildState() != buildBefore {
		t.Error("BuildState instance changed after Reconnect")
	}
	if mgr.ExecState() != execBefore {
		t.Error("ExecState instance changed after Reconnect")
	}
	if mgr.NetworkState() != netBefore {
		t.Error("NetworkState instance changed after Reconnect")
	}
	if mgr.VolumeState() != volBefore {
		t.Error("VolumeState instance changed after Reconnect")
	}
}

func TestReconnect_StateHistoryPreserved(t *testing.T) {
	requireDocker(t)
	mgr := newManager(t)

	// Record a state change directly — no Docker operation needed.
	imgState := mgr.ImageState()
	change := imgState.RequestChange(dockerimagestate.OpPull, "test:latest", nil)
	if _, err := imgState.RecordFailure(change.ID, errors.New("simulated failure")); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}

	_, _, failBefore, _ := imgState.Summary()
	if failBefore == 0 {
		t.Fatal("expected at least one failed record before Reconnect")
	}

	if err := mgr.Reconnect(ctx10s(t)); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}

	_, _, failAfter, _ := mgr.ImageState().Summary()
	if failAfter != failBefore {
		t.Errorf("failed record count changed after Reconnect: before=%d after=%d",
			failBefore, failAfter)
	}
}

func TestReconnect_ImageStorePreservedAcrossReconnect(t *testing.T) {
	requireDocker(t)
	mgr := newManager(t)

	// Register a synthetic image in the store.
	const syntheticID = "sha256:deadbeef0000000000000000000000000000000000000000000000000000cafe"
	mgr.Images().Store().Register(dockerimagestate.RegisterParams{
		DockerID: syntheticID,
		Origin:   dockerimagestate.OriginInBand,
	})

	if _, ok := mgr.Images().Store().Get(syntheticID); !ok {
		t.Fatal("synthetic record not found before Reconnect")
	}

	if err := mgr.Reconnect(ctx10s(t)); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}

	// After reconnect the store is the same object so the record persists.
	// Refresh will mark it deleted_out_of_band (Docker doesn't know it), but
	// the record itself must still be in the store.
	rec, ok := mgr.Images().Store().Get(syntheticID)
	if !ok {
		t.Fatal("synthetic image record lost after Reconnect")
	}
	// Refresh detected Docker doesn't have this ID → out-of-band deletion.
	if rec.Status != dockerimagestate.ImageStatusDeletedOutOfBand {
		t.Errorf("expected synthetic record to be deleted_out_of_band after Reconnect, got %q", rec.Status)
	}
}

func TestReconnect_PingSucceedsAfterReconnect(t *testing.T) {
	requireDocker(t)
	mgr := newManager(t)
	ctx := ctx10s(t)

	if err := mgr.Reconnect(ctx); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}

	if err := mgr.Ping(ctx); err != nil {
		t.Errorf("Ping after Reconnect: %v", err)
	}
}

// ── Close ─────────────────────────────────────────────────────────────────────

func TestClose_IsIdempotent(t *testing.T) {
	requireDocker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mgr, err := dockermanager.New(ctx, silentLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for i, name := range []string{"first", "second", "third"} {
		if err := mgr.Close(); err != nil {
			t.Errorf("%s Close (call %d): %v", name, i+1, err)
		}
	}
}

func TestClose_SubsystemsNilAfterClose(t *testing.T) {
	requireDocker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mgr, err := dockermanager.New(ctx, silentLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := mgr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if mgr.Images() != nil {
		t.Error("Images() should be nil after Close")
	}
	if mgr.Builds() != nil {
		t.Error("Builds() should be nil after Close")
	}
	if mgr.Networks() != nil {
		t.Error("Networks() should be nil after Close")
	}
	if mgr.Volumes() != nil {
		t.Error("Volumes() should be nil after Close")
	}
	if mgr.Containers() != nil {
		t.Error("Containers() should be nil after Close")
	}
	if mgr.Exec() != nil {
		t.Error("Exec() should be nil after Close")
	}
}

func TestClose_StateInstancesPreservedAfterClose(t *testing.T) {
	requireDocker(t)
	mgr := newManager(t)

	imgBefore := mgr.ImageState()

	if err := mgr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// States outlive the connection — they are the historical record.
	if mgr.ImageState() != imgBefore {
		t.Error("ImageState instance changed after Close")
	}
	// Must be callable without panicking.
	_, _, _, _ = mgr.ImageState().Summary()
}

func TestClose_StoreSurvivesManagerClose(t *testing.T) {
	requireDocker(t)
	mgr := newManager(t)

	// Capture a direct reference to the image store before Close.
	store := mgr.Images().Store()
	sizeBefore := store.Size()

	if err := mgr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// mgr.Images() is now nil, but the store object is still live and readable.
	sizeAfter := store.Size()
	if sizeAfter != sizeBefore {
		t.Errorf("store size changed after Close: before=%d after=%d", sizeBefore, sizeAfter)
	}
}

// ── Concurrency ───────────────────────────────────────────────────────────────

func TestConcurrentAccessors_NoRace(t *testing.T) {
	requireDocker(t)
	mgr := newManager(t)

	const goroutines = 30
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = mgr.Images()
			_ = mgr.Containers()
			_ = mgr.Builds()
			_ = mgr.Exec()
			_ = mgr.Networks()
			_ = mgr.Volumes()
			_ = mgr.ImageState()
			_ = mgr.ContainerState()
			_ = mgr.BuildState()
			_ = mgr.ExecState()
			_ = mgr.NetworkState()
			_ = mgr.VolumeState()
		}()
	}
	wg.Wait()
}

func TestConcurrentPingAndAccessors_NoRace(t *testing.T) {
	requireDocker(t)
	mgr := newManager(t)
	ctx := ctx10s(t)

	var wg sync.WaitGroup

	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = mgr.Images()
			_ = mgr.Containers()
			_ = mgr.NetworkState()
		}()
	}
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = mgr.Ping(ctx)
		}()
	}

	wg.Wait()
}

func TestConcurrentReconnectAndAccessors_NoRace(t *testing.T) {
	requireDocker(t)
	mgr := newManager(t)
	ctx := ctx10s(t)

	var wg sync.WaitGroup

	// One goroutine reconnects while others read accessor fields.
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = mgr.Reconnect(ctx)
	}()

	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Accessor reads race with the reconnect write; the RWMutex must
			// protect these correctly.
			_ = mgr.Images()
			_ = mgr.Containers()
			_ = mgr.ImageState()
		}()
	}

	wg.Wait()
}
