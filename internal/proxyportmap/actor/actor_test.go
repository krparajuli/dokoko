package proxyportmapactor

import (
	"context"
	"errors"
	"testing"
	"time"

	portproxyactor "dokoko.ai/dokoko/internal/portproxy/actor"
	portproxystate "dokoko.ai/dokoko/internal/portproxy/state"
	proxyportmapstate "dokoko.ai/dokoko/internal/proxyportmap/state"
	"dokoko.ai/dokoko/pkg/logger"
)

// ── Test helpers ──────────────────────────────────────────────────────────────

// closedTicket returns a *portproxyactor.Ticket whose Done channel is already
// closed so that ticket.Wait() returns immediately with nil.
func closedTicket() *portproxyactor.Ticket {
	ch := make(chan struct{})
	close(ch)
	return &portproxyactor.Ticket{ChangeID: "test", Done: ch}
}

func newTestLogger() *logger.Logger { return logger.New(logger.LevelError) }

// ── Mock implementations ───────────────────────────────────────────────────────

type mockOps struct {
	ports []uint16
	err   error
}

func (m *mockOps) ScanListeningPorts(_ context.Context, _ string) ([]uint16, error) {
	return m.ports, m.err
}

type mockProxy struct {
	ensureErr    error
	registerErr  error
	store        *portproxystate.Store
	registerFunc func(name, id string, ports []portproxystate.ContainerPort)
}

func (m *mockProxy) EnsureProxy(_ context.Context) (*portproxyactor.Ticket, error) {
	if m.ensureErr != nil {
		return nil, m.ensureErr
	}
	return closedTicket(), nil
}

func (m *mockProxy) RegisterContainer(_ context.Context, name, id string, ports []portproxystate.ContainerPort) (*portproxyactor.Ticket, error) {
	if m.registerFunc != nil {
		m.registerFunc(name, id, ports)
	}
	if m.registerErr != nil {
		return nil, m.registerErr
	}
	return closedTicket(), nil
}

func (m *mockProxy) DeregisterContainer(_ context.Context, _ string) (*portproxyactor.Ticket, error) {
	return closedTicket(), nil
}

func (m *mockProxy) Store() *portproxystate.Store {
	return m.store
}

// ── Constructor helper ─────────────────────────────────────────────────────────

func newActor(ops opsProvider, pp proxyRegistrar) *Actor {
	log := newTestLogger()
	st := proxyportmapstate.New(log)
	store := proxyportmapstate.NewStore(log)
	return New(ops, pp, st, store, log, &Config{Workers: 1, QueueSize: 8})
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestScanAndMap_noPorts verifies that a scan finding zero ports stores an
// empty (non-nil) result immediately.
func TestScanAndMap_noPorts(t *testing.T) {
	actor := newActor(&mockOps{ports: nil}, &mockProxy{store: portproxystate.NewStore(newTestLogger())})
	defer actor.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ticket, err := actor.ScanAndMap(ctx, "user1", "c1", "cid1")
	if err != nil {
		t.Fatalf("ScanAndMap returned unexpected error: %v", err)
	}
	if err := ticket.Wait(ctx); err != nil {
		t.Fatalf("ticket.Wait returned unexpected error: %v", err)
	}

	result := actor.store.GetResult("user1")
	if result == nil {
		t.Fatal("expected a result to be stored, got nil")
	}
	if len(result.Ports) != 0 {
		t.Errorf("expected 0 ports in result, got %d", len(result.Ports))
	}
}

// TestScanAndMap_storesRawPortsOnProxyFailure is the critical regression test:
// when proxy registration fails, the scan result must still be stored so the
// caller can show the user which ports were found.
func TestScanAndMap_storesRawPortsOnProxyFailure(t *testing.T) {
	ops := &mockOps{ports: []uint16{8080, 3000}}
	pp := &mockProxy{
		ensureErr: errors.New("proxy container unavailable"),
		store:     portproxystate.NewStore(newTestLogger()),
	}
	actor := newActor(ops, pp)
	defer actor.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ticket, err := actor.ScanAndMap(ctx, "user2", "c2", "cid2")
	if err != nil {
		t.Fatalf("ScanAndMap returned unexpected error: %v", err)
	}
	if err := ticket.Wait(ctx); err != nil {
		t.Fatalf("ticket.Wait returned unexpected error: %v", err)
	}

	result := actor.store.GetResult("user2")
	if result == nil {
		t.Fatal("expected result to be stored even though proxy failed, got nil")
	}
	if len(result.Ports) != 2 {
		t.Fatalf("expected 2 ports in result, got %d: %+v", len(result.Ports), result.Ports)
	}
	// HostPort should be 0 because proxy registration never completed.
	for _, p := range result.Ports {
		if p.HostPort != 0 {
			t.Errorf("expected HostPort=0 (proxy failed) for container port %d, got %d", p.ContainerPort, p.HostPort)
		}
	}
}

// TestScanAndMap_storesRawPortsOnRegisterFailure verifies the same guarantee
// when EnsureProxy succeeds but RegisterContainer fails.
func TestScanAndMap_storesRawPortsOnRegisterFailure(t *testing.T) {
	ops := &mockOps{ports: []uint16{5000}}
	pp := &mockProxy{
		registerErr: errors.New("docker network error"),
		store:       portproxystate.NewStore(newTestLogger()),
	}
	actor := newActor(ops, pp)
	defer actor.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ticket, err := actor.ScanAndMap(ctx, "user3", "c3", "cid3")
	if err != nil {
		t.Fatalf("ScanAndMap returned unexpected error: %v", err)
	}
	if err := ticket.Wait(ctx); err != nil {
		t.Fatalf("ticket.Wait returned unexpected error: %v", err)
	}

	result := actor.store.GetResult("user3")
	if result == nil {
		t.Fatal("expected result stored even though registration failed, got nil")
	}
	if len(result.Ports) != 1 || result.Ports[0].ContainerPort != 5000 {
		t.Errorf("unexpected result ports: %+v", result.Ports)
	}
}

// TestScanAndMap_updatesHostPortsOnSuccess verifies that when proxy
// registration succeeds the result is updated with the allocated host ports.
func TestScanAndMap_updatesHostPortsOnSuccess(t *testing.T) {
	ops := &mockOps{ports: []uint16{8080}}
	ppStore := portproxystate.NewStore(newTestLogger())

	var registeredName string
	pp := &mockProxy{
		store: ppStore,
		registerFunc: func(name, id string, ports []portproxystate.ContainerPort) {
			registeredName = name
			// Simulate the portproxy store allocating a host port.
			_, _ = ppStore.AllocatePort(name, id, ports[0])
		},
	}
	actor := newActor(ops, pp)
	defer actor.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ticket, err := actor.ScanAndMap(ctx, "user4", "c4", "cid4")
	if err != nil {
		t.Fatalf("ScanAndMap returned unexpected error: %v", err)
	}
	if err := ticket.Wait(ctx); err != nil {
		t.Fatalf("ticket.Wait returned unexpected error: %v", err)
	}

	if registeredName != "c4" {
		t.Errorf("expected container c4 to be registered, got %q", registeredName)
	}

	result := actor.store.GetResult("user4")
	if result == nil {
		t.Fatal("expected result to be stored")
	}
	if len(result.Ports) != 1 {
		t.Fatalf("expected 1 port, got %d: %+v", len(result.Ports), result.Ports)
	}
	if result.Ports[0].ContainerPort != 8080 {
		t.Errorf("expected ContainerPort=8080, got %d", result.Ports[0].ContainerPort)
	}
	if result.Ports[0].HostPort == 0 {
		t.Errorf("expected HostPort to be set (proxy succeeded), got 0")
	}
}

// TestUnmap_clearsResult verifies that Unmap removes the stored result.
func TestUnmap_clearsResult(t *testing.T) {
	ops := &mockOps{ports: []uint16{9000}}
	pp := &mockProxy{
		ensureErr: errors.New("proxy not needed for this test"),
		store:     portproxystate.NewStore(newTestLogger()),
	}
	actor := newActor(ops, pp)
	defer actor.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Seed a result directly.
	actor.store.SetResult(&proxyportmapstate.ScanResult{
		UserID:        "user5",
		ContainerName: "c5",
		ContainerID:   "cid5",
		Ports:         []proxyportmapstate.MappedPort{{ContainerPort: 9000}},
		ScannedAt:     time.Now(),
	})

	ticket, err := actor.Unmap(ctx, "user5")
	if err != nil {
		t.Fatalf("Unmap returned unexpected error: %v", err)
	}
	if err := ticket.Wait(ctx); err != nil {
		t.Fatalf("ticket.Wait returned unexpected error: %v", err)
	}

	if result := actor.store.GetResult("user5"); result != nil {
		t.Errorf("expected result to be cleared after Unmap, got %+v", result)
	}
}
