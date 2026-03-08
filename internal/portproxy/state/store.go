package portproxystate

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"dokoko.ai/dokoko/pkg/logger"
)

// ── Port range ────────────────────────────────────────────────────────────────

const (
	// PortRangeStart is the first host port available for proxy allocation.
	PortRangeStart = 8100
	// PortRangeEnd is the last host port available for proxy allocation.
	PortRangeEnd = 8199
)

// ErrPortRangeExhausted is returned when no free host port is available.
var ErrPortRangeExhausted = errors.New("portproxy: port range 8100-8199 exhausted")

// ── Types ─────────────────────────────────────────────────────────────────────

// ContainerPort identifies a single exposed port on a container.
type ContainerPort struct {
	Port  uint16
	Proto string // "tcp" or "udp"
}

// MappingStatus describes the lifecycle state of a PortMapping.
type MappingStatus string

const (
	MappingStatusActive  MappingStatus = "active"
	MappingStatusRemoved MappingStatus = "removed"
)

// PortMapping records the allocation of a single host port to a container port.
type PortMapping struct {
	ContainerName string
	ContainerID   string
	ContainerPort ContainerPort
	HostPort      uint16        // allocated from [PortRangeStart, PortRangeEnd]
	NetworkName   string        // "proxy_<ContainerName>"
	Status        MappingStatus
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// mappingKey builds the canonical map key for a container+port combination.
func mappingKey(containerName string, cp ContainerPort) string {
	return fmt.Sprintf("%s:%d/%s", containerName, cp.Port, cp.Proto)
}

// ── Store ─────────────────────────────────────────────────────────────────────

// Store is the in-memory registry of port mappings and the host-port allocator.
// All exported methods are safe for concurrent use.
type Store struct {
	mu        sync.RWMutex
	mappings  map[string]*PortMapping // key: "<containerName>:<port>/<proto>"
	allocated map[uint16]string       // hostPort → mapping key (for dedup)
	log       *logger.Logger
}

// NewStore returns an empty, ready-to-use Store.
func NewStore(log *logger.Logger) *Store {
	log.LowTrace("initialising portproxy store")
	s := &Store{
		mappings:  make(map[string]*PortMapping),
		allocated: make(map[uint16]string),
		log:       log,
	}
	log.Info("portproxy store ready")
	return s
}

// ── Mutations ─────────────────────────────────────────────────────────────────

// AllocatePort finds the lowest free port in [PortRangeStart, PortRangeEnd],
// records it, and returns the new PortMapping.  Returns ErrPortRangeExhausted
// when all 100 ports are in use.
//
// If an identical mapping (same containerName + containerPort) already exists
// and is active, the existing mapping is returned without allocating a new port.
func (s *Store) AllocatePort(containerName, containerID string, cp ContainerPort) (*PortMapping, error) {
	s.log.LowTrace("portproxy store: allocating port for %s:%d/%s", containerName, cp.Port, cp.Proto)

	key := mappingKey(containerName, cp)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Return existing active mapping (idempotent).
	if existing, ok := s.mappings[key]; ok && existing.Status == MappingStatusActive {
		s.log.Debug("portproxy store: reusing existing mapping hostPort=%d for %s", existing.HostPort, key)
		return existing, nil
	}

	// Find the lowest free host port.
	var hostPort uint16
	found := false
	for p := uint16(PortRangeStart); p <= uint16(PortRangeEnd); p++ {
		if _, used := s.allocated[p]; !used {
			hostPort = p
			found = true
			break
		}
	}
	if !found {
		s.log.Warn("portproxy store: port range exhausted")
		return nil, ErrPortRangeExhausted
	}

	now := time.Now()
	m := &PortMapping{
		ContainerName: containerName,
		ContainerID:   containerID,
		ContainerPort: cp,
		HostPort:      hostPort,
		NetworkName:   "proxy_" + containerName,
		Status:        MappingStatusActive,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	s.mappings[key] = m
	s.allocated[hostPort] = key

	s.log.Info("portproxy store: allocated hostPort=%d for %s (container=%s)", hostPort, key, containerName)
	return m, nil
}

// ReleaseMappingsFor marks all active mappings for containerName as removed,
// frees their host ports, and returns the freed list.
func (s *Store) ReleaseMappingsFor(containerName string) []*PortMapping {
	s.log.LowTrace("portproxy store: releasing mappings for %s", containerName)

	s.mu.Lock()
	defer s.mu.Unlock()

	var freed []*PortMapping
	now := time.Now()
	for key, m := range s.mappings {
		if m.ContainerName == containerName && m.Status == MappingStatusActive {
			delete(s.allocated, m.HostPort)
			m.Status = MappingStatusRemoved
			m.UpdatedAt = now
			freed = append(freed, m)
			s.log.Debug("portproxy store: released hostPort=%d for %s", m.HostPort, key)
		}
	}

	s.log.Info("portproxy store: released %d mapping(s) for %s", len(freed), containerName)
	return freed
}

// ── Queries ───────────────────────────────────────────────────────────────────

// AllActive returns snapshot copies of all active mappings.
// Used to regenerate the nginx configuration.
func (s *Store) AllActive() []*PortMapping {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []*PortMapping
	for _, m := range s.mappings {
		if m.Status == MappingStatusActive {
			cp := *m
			out = append(out, &cp)
		}
	}
	return out
}

// GetByContainer returns snapshot copies of all mappings (any status) for
// the named container.
func (s *Store) GetByContainer(containerName string) []*PortMapping {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []*PortMapping
	for _, m := range s.mappings {
		if m.ContainerName == containerName {
			cp := *m
			out = append(out, &cp)
		}
	}
	return out
}
