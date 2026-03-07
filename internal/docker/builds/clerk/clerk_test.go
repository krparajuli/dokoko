package dockerbuildclerk_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	dockerbuildclerk "dokoko.ai/dokoko/internal/docker/builds/clerk"
	dockerbuildactor "dokoko.ai/dokoko/internal/docker/builds/actor"
	dockerbuildops "dokoko.ai/dokoko/internal/docker/builds/ops"
	dockerbuildstate "dokoko.ai/dokoko/internal/docker/builds/state"
	"dokoko.ai/dokoko/pkg/logger"
	dockertypes "github.com/docker/docker/api/types"
)

// ── fakeBuildActor ────────────────────────────────────────────────────────────

// fakeBuildActor mirrors the real actor's state-registration behaviour so the
// clerk's watcher can call state.FindByID successfully.
type fakeBuildActor struct {
	state    *dockerbuildstate.State
	buildErr error // if non-nil, Build returns this error

	// dones holds the bidirectional done channels keyed by ChangeID so that
	// tests can close them (tickets expose only the receive direction).
	dones map[string]chan struct{}
}

func newFake(st *dockerbuildstate.State) *fakeBuildActor {
	return &fakeBuildActor{state: st, dones: make(map[string]chan struct{})}
}

// closeTicket closes the done channel for changeID, unblocking the watcher
// goroutine and any callers of ticket.Wait.
func (f *fakeBuildActor) closeTicket(changeID string) {
	if ch, ok := f.dones[changeID]; ok {
		close(ch)
		delete(f.dones, changeID)
	}
}

func (f *fakeBuildActor) Build(ctx context.Context, req dockerbuildops.BuildRequest) (*dockerbuildactor.Ticket, error) {
	if f.buildErr != nil {
		return nil, f.buildErr
	}
	tags := strings.Join(req.Tags, ",")
	change := f.state.RequestChange(dockerbuildstate.OpBuild, tags, nil)
	done := make(chan struct{})
	f.dones[change.ID] = done
	return &dockerbuildactor.Ticket{ChangeID: change.ID, Done: done}, nil
}

func (f *fakeBuildActor) PruneCache(ctx context.Context, opts dockertypes.BuildCachePruneOptions) (*dockerbuildactor.Ticket, error) {
	change := f.state.RequestChange(dockerbuildstate.OpPruneCache, "", nil)
	done := make(chan struct{})
	close(done) // prune settles immediately in the fake
	_, _ = f.state.ConfirmSuccess(change.ID, "")
	return &dockerbuildactor.Ticket{ChangeID: change.ID, Done: done}, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func silentLogger() *logger.Logger { return logger.New(logger.LevelSilent) }

func newTestClerk(fa *fakeBuildActor, st *dockerbuildstate.State) (*dockerbuildclerk.Clerk, *dockerbuildstate.BuildStore) {
	store := dockerbuildstate.NewBuildStore(silentLogger())
	cl := dockerbuildclerk.New(fa, st, store, silentLogger())
	return cl, store
}

func waitTicket(t *testing.T, ticket *dockerbuildactor.Ticket) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ticket.Wait(ctx); err != nil {
		t.Fatalf("ticket.Wait: %v", err)
	}
}

// ── Build — store registration ────────────────────────────────────────────────

func TestBuild_RegistersPendingInStore(t *testing.T) {
	st := dockerbuildstate.New(silentLogger())
	fa := newFake(st)
	cl, store := newTestClerk(fa, st)

	ticket, err := cl.Build(context.Background(), dockerbuildops.BuildRequest{
		ContextDir: "/tmp",
		Tags:       []string{"myapp:1.0"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	rec, ok := store.Get(ticket.ChangeID)
	if !ok {
		t.Fatal("expected build record in store immediately after Build()")
	}
	if rec.Status != dockerbuildstate.BuildStatusPending {
		t.Errorf("initial status: got %q, want pending", rec.Status)
	}
	if len(rec.Tags) == 0 || rec.Tags[0] != "myapp:1.0" {
		t.Errorf("Tags: got %v, want [myapp:1.0]", rec.Tags)
	}
	if rec.ContextDir != "/tmp" {
		t.Errorf("ContextDir: got %q, want /tmp", rec.ContextDir)
	}

	// Settle so the watcher goroutine can exit before Close().
	_, _ = st.Abandon(ticket.ChangeID, "test cleanup")
	fa.closeTicket(ticket.ChangeID)
	cl.Close()
}

func TestBuild_RemoteContextStoredInContextDir(t *testing.T) {
	st := dockerbuildstate.New(silentLogger())
	fa := newFake(st)
	cl, store := newTestClerk(fa, st)

	ticket, err := cl.Build(context.Background(), dockerbuildops.BuildRequest{
		RemoteContext: "https://github.com/example/repo.git",
		Tags:          []string{"from-git:latest"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	rec, ok := store.Get(ticket.ChangeID)
	if !ok {
		t.Fatal("expected build record in store")
	}
	if rec.ContextDir != "https://github.com/example/repo.git" {
		t.Errorf("ContextDir: got %q, want the remote URL", rec.ContextDir)
	}

	// Settle so the watcher goroutine can exit before Close().
	_, _ = st.Abandon(ticket.ChangeID, "test cleanup")
	fa.closeTicket(ticket.ChangeID)
	cl.Close()
}

// ── Build — watcher lifecycle ─────────────────────────────────────────────────

func TestBuild_SuccessTransitionsStoreToSucceeded(t *testing.T) {
	st := dockerbuildstate.New(silentLogger())
	fa := newFake(st)
	cl, store := newTestClerk(fa, st)

	ticket, err := cl.Build(context.Background(), dockerbuildops.BuildRequest{
		ContextDir: "/tmp",
		Tags:       []string{"myapp:2.0"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Confirm the change in state then close the ticket.
	if _, err := st.ConfirmSuccess(ticket.ChangeID, "sha256:deadbeef1234"); err != nil {
		t.Fatalf("ConfirmSuccess: %v", err)
	}
	fa.closeTicket(ticket.ChangeID)

	waitTicket(t, ticket)
	cl.Close() // drain the watcher goroutine before asserting

	rec, ok := store.Get(ticket.ChangeID)
	if !ok {
		t.Fatal("expected build record in store after settle")
	}
	if rec.Status != dockerbuildstate.BuildStatusSucceeded {
		t.Errorf("status: got %q, want succeeded", rec.Status)
	}
	if rec.ResultImageID != "sha256:deadbeef1234" {
		t.Errorf("ResultImageID: got %q, want sha256:deadbeef1234", rec.ResultImageID)
	}
}

func TestBuild_FailureTransitionsStoreToFailed(t *testing.T) {
	st := dockerbuildstate.New(silentLogger())
	fa := newFake(st)
	cl, store := newTestClerk(fa, st)

	ticket, err := cl.Build(context.Background(), dockerbuildops.BuildRequest{
		ContextDir: "/tmp",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if _, err := st.RecordFailure(ticket.ChangeID, errors.New("Dockerfile not found")); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	fa.closeTicket(ticket.ChangeID)

	waitTicket(t, ticket)
	cl.Close()

	rec, ok := store.Get(ticket.ChangeID)
	if !ok {
		t.Fatal("expected build record")
	}
	if rec.Status != dockerbuildstate.BuildStatusFailed {
		t.Errorf("status: got %q, want failed", rec.Status)
	}
	if rec.ErrMsg == "" {
		t.Error("ErrMsg should not be empty on failure")
	}
}

func TestBuild_AbandonedTransitionsStoreToAbandoned(t *testing.T) {
	st := dockerbuildstate.New(silentLogger())
	fa := newFake(st)
	cl, store := newTestClerk(fa, st)

	ticket, err := cl.Build(context.Background(), dockerbuildops.BuildRequest{
		ContextDir: "/tmp",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if _, err := st.Abandon(ticket.ChangeID, "context cancelled"); err != nil {
		t.Fatalf("Abandon: %v", err)
	}
	fa.closeTicket(ticket.ChangeID)

	waitTicket(t, ticket)
	cl.Close()

	rec, ok := store.Get(ticket.ChangeID)
	if !ok {
		t.Fatal("expected build record")
	}
	if rec.Status != dockerbuildstate.BuildStatusAbandoned {
		t.Errorf("status: got %q, want abandoned", rec.Status)
	}
	if rec.ErrMsg == "" {
		t.Error("ErrMsg should contain the abandon reason")
	}
}

// ── Build — error propagation ─────────────────────────────────────────────────

func TestBuild_ActorErrorPropagated(t *testing.T) {
	st := dockerbuildstate.New(silentLogger())
	fa := newFake(st)
	fa.buildErr = errors.New("queue full")
	cl, _ := newTestClerk(fa, st)
	defer cl.Close()

	_, err := cl.Build(context.Background(), dockerbuildops.BuildRequest{ContextDir: "/tmp"})
	if err == nil {
		t.Error("expected error from Build when actor fails")
	}
}

// ── PruneCache ────────────────────────────────────────────────────────────────

func TestPruneCache_DelegatesToActor(t *testing.T) {
	st := dockerbuildstate.New(silentLogger())
	fa := newFake(st)
	cl, _ := newTestClerk(fa, st)
	defer cl.Close()

	ticket, err := cl.PruneCache(context.Background(), dockertypes.BuildCachePruneOptions{})
	if err != nil {
		t.Fatalf("PruneCache: %v", err)
	}
	if ticket == nil {
		t.Fatal("expected non-nil ticket")
	}

	waitTicket(t, ticket)
}

func TestPruneCache_NotTrackedInBuildStore(t *testing.T) {
	st := dockerbuildstate.New(silentLogger())
	fa := newFake(st)
	cl, store := newTestClerk(fa, st)
	defer cl.Close()

	ticket, err := cl.PruneCache(context.Background(), dockertypes.BuildCachePruneOptions{})
	if err != nil {
		t.Fatalf("PruneCache: %v", err)
	}

	// PruneCache should not register anything in the build store.
	if _, ok := store.Get(ticket.ChangeID); ok {
		t.Error("PruneCache should not create a build record in the store")
	}
}

// ── Close ─────────────────────────────────────────────────────────────────────

func TestClose_IsIdempotent(t *testing.T) {
	st := dockerbuildstate.New(silentLogger())
	fa := newFake(st)
	cl, _ := newTestClerk(fa, st)
	cl.Close()
	cl.Close()
	cl.Close()
}

// ── Accessors ─────────────────────────────────────────────────────────────────

func TestState_ReturnsSameInstance(t *testing.T) {
	st := dockerbuildstate.New(silentLogger())
	fa := newFake(st)
	cl, _ := newTestClerk(fa, st)
	defer cl.Close()

	if cl.State() != st {
		t.Error("State() returned wrong instance")
	}
}

func TestBuildStore_ReturnsSameInstance(t *testing.T) {
	st := dockerbuildstate.New(silentLogger())
	fa := newFake(st)
	cl, store := newTestClerk(fa, st)
	defer cl.Close()

	if cl.BuildStore() != store {
		t.Error("BuildStore() returned wrong instance")
	}
}
