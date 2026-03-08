package proxyportmapstate

import (
	"sync"
	"time"

	"dokoko.ai/dokoko/pkg/logger"
)

// MappedPort is a single container TCP port that has been registered with the
// proxy; HostPort is the published port on the Docker host (in 8100–8199).
type MappedPort struct {
	ContainerPort uint16
	HostPort      uint16
}

// ScanResult records the most-recent port scan outcome for a user's container.
type ScanResult struct {
	UserID        string
	ContainerName string
	ContainerID   string
	Ports         []MappedPort
	ScannedAt     time.Time
	UpdatedAt     time.Time
}

// Store is the in-memory registry of ScanResult records.
// All exported methods are safe for concurrent use.
type Store struct {
	mu      sync.RWMutex
	results map[string]*ScanResult // key: UserID
	log     *logger.Logger
}

// NewStore returns an empty, ready-to-use Store.
func NewStore(log *logger.Logger) *Store {
	log.LowTrace("initialising proxyportmap store")
	s := &Store{
		results: make(map[string]*ScanResult),
		log:     log,
	}
	log.Info("proxyportmap store ready")
	return s
}

// SetResult creates or replaces the ScanResult for result.UserID.
func (s *Store) SetResult(result *ScanResult) {
	cp := *result
	cp.UpdatedAt = time.Now()
	ports := make([]MappedPort, len(result.Ports))
	copy(ports, result.Ports)
	cp.Ports = ports

	s.mu.Lock()
	s.results[result.UserID] = &cp
	s.mu.Unlock()

	s.log.Debug("proxyportmap store: set result user=%s ports=%d", result.UserID, len(result.Ports))
}

// GetResult returns the ScanResult for userID, or nil if absent.
// The returned value is a copy; callers may modify it safely.
func (s *Store) GetResult(userID string) *ScanResult {
	s.mu.RLock()
	r := s.results[userID]
	s.mu.RUnlock()

	if r == nil {
		return nil
	}
	cp := *r
	ports := make([]MappedPort, len(r.Ports))
	copy(ports, r.Ports)
	cp.Ports = ports
	return &cp
}

// DeleteResult removes the ScanResult for userID.
func (s *Store) DeleteResult(userID string) {
	s.mu.Lock()
	delete(s.results, userID)
	s.mu.Unlock()
	s.log.Debug("proxyportmap store: deleted result user=%s", userID)
}

// AllResults returns snapshot copies of all stored results.
func (s *Store) AllResults() []*ScanResult {
	s.mu.RLock()
	out := make([]*ScanResult, 0, len(s.results))
	for _, r := range s.results {
		cp := *r
		ports := make([]MappedPort, len(r.Ports))
		copy(ports, r.Ports)
		cp.Ports = ports
		out = append(out, &cp)
	}
	s.mu.RUnlock()
	return out
}
