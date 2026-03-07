// store.go — in-process network inventory with lifecycle and origin tracking.
//
// The Store tracks every network this process has ever observed, independently
// of Docker itself.  Networks are keyed by their Docker-assigned ID (a stable
// content-addressed hash).
//
// Lifecycle diagram:
//
//	Register()
//	  ──► [present]
//	           │
//	    ┌──────┼──────────────────┐
//	    ▼      ▼                  ▼
//	[deleted] [deleted_out_of_band] [errored]
//	    │                          │
//	    └──────────────────────────┘
//	              │
//	           Register()   ← re-create restores to present
//	              ▼
//	          [present]

package dockernetworkstate

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"dokoko.ai/dokoko/pkg/logger"
)

// ── Errors ────────────────────────────────────────────────────────────────────

// ErrNetworkNotFound is returned by Mark* operations when the DockerID is absent.
var ErrNetworkNotFound = errors.New("network not found in store")

// ── NetworkOrigin ─────────────────────────────────────────────────────────────

// NetworkOrigin describes how a network entered the store.
type NetworkOrigin string

const (
	// NetworkOriginInBand means the network was created through dokoko.
	NetworkOriginInBand NetworkOrigin = "in_band"

	// NetworkOriginOutOfBand means the network was already present in Docker
	// when first observed, or appeared without going through dokoko.
	NetworkOriginOutOfBand NetworkOrigin = "out_of_band"
)

// ── NetworkStatus ─────────────────────────────────────────────────────────────

// NetworkStatus describes the known lifecycle state of a tracked network.
type NetworkStatus string

const (
	// NetworkStatusPresent means the network is confirmed present in Docker.
	NetworkStatusPresent NetworkStatus = "present"

	// NetworkStatusDeleted means the network was removed through dokoko (in-band).
	NetworkStatusDeleted NetworkStatus = "deleted"

	// NetworkStatusDeletedOutOfBand means the network disappeared from Docker
	// without going through dokoko, detected during Reconcile.
	NetworkStatusDeletedOutOfBand NetworkStatus = "deleted_out_of_band"

	// NetworkStatusErrored means an operation on this network resulted in an
	// error.  ErrMsg on the record holds the details.
	NetworkStatusErrored NetworkStatus = "errored"
)

// ── RegisterNetworkParams ─────────────────────────────────────────────────────

// RegisterNetworkParams carries all metadata needed to upsert a network record.
// Populate it from dockertypes.NetworkResource at the call site.
type RegisterNetworkParams struct {
	DockerID   string        // Docker-assigned network ID (stable hash)
	Name       string        // human-readable network name
	Driver     string        // e.g. "bridge", "overlay", "host"
	Scope      string        // e.g. "local", "global", "swarm"
	Internal   bool          // true if the network is internal (no outbound routing)
	Attachable bool          // true if standalone containers can attach
	EnableIPv6 bool          // true if IPv6 is enabled
	Origin     NetworkOrigin // InBand or OutOfBand
}

// ── NetworkRecord ─────────────────────────────────────────────────────────────

// NetworkRecord is the store's complete view of one tracked network.
// Callers receive copies; the internal pointer is never exposed.
type NetworkRecord struct {
	// Identity
	DockerID string // Docker-assigned network ID
	ShortID  string // first 12 hex chars (for display)
	Name     string // human-readable name

	// Docker metadata
	Driver     string // network driver
	Scope      string // "local", "global", "swarm"
	Internal   bool   // no external routing
	Attachable bool   // standalone containers can attach
	EnableIPv6 bool   // IPv6 enabled

	// Timestamps
	RegisteredAt time.Time // first time this store observed the network
	UpdatedAt    time.Time // last mutation of any field

	// Lifecycle
	Origin NetworkOrigin // how this network entered the store
	Status NetworkStatus // current known state
	ErrMsg string        // non-empty only when Status == NetworkStatusErrored
}

// clone returns a safe snapshot of the record.
func (r *NetworkRecord) clone() *NetworkRecord {
	cp := *r
	return &cp
}

// ── NetworkStore ──────────────────────────────────────────────────────────────

// NetworkStore is an in-memory registry of all networks known to this process.
// All exported methods are safe for concurrent use.
type NetworkStore struct {
	mu      sync.RWMutex
	records map[string]*NetworkRecord // keyed by DockerID
	log     *logger.Logger
}

// NewNetworkStore returns an empty, ready-to-use NetworkStore.
func NewNetworkStore(log *logger.Logger) *NetworkStore {
	log.LowTrace("initialising network store")
	s := &NetworkStore{
		records: make(map[string]*NetworkRecord),
		log:     log,
	}
	log.Info("network store ready")
	return s
}

// ── Mutations ─────────────────────────────────────────────────────────────────

// Register upserts a network record.
//
// First call: creates a new record with Status=Present.
// Subsequent calls: updates mutable fields and sets Status back to Present.
// Origin is upgraded from OutOfBand to InBand; never downgraded.
func (s *NetworkStore) Register(p RegisterNetworkParams) *NetworkRecord {
	s.log.LowTrace("register: dockerID=%s name=%q origin=%s",
		shortNetID(p.DockerID), p.Name, p.Origin)

	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	rec, exists := s.records[p.DockerID]

	if !exists {
		rec = &NetworkRecord{
			DockerID:     p.DockerID,
			ShortID:      shortNetID(p.DockerID),
			RegisteredAt: now,
			Origin:       p.Origin,
		}
		s.log.Debug("register: new network record: id=%s name=%q origin=%s",
			rec.ShortID, p.Name, p.Origin)
	} else {
		if p.Origin == NetworkOriginInBand && rec.Origin == NetworkOriginOutOfBand {
			s.log.Debug("register: upgrading origin OutOfBand→InBand for %s", rec.ShortID)
			rec.Origin = NetworkOriginInBand
		}
		s.log.Debug("register: updating network record: id=%s prev_status=%s",
			rec.ShortID, rec.Status)
	}

	// Always refresh mutable fields.
	rec.Name = p.Name
	rec.Driver = p.Driver
	rec.Scope = p.Scope
	rec.Internal = p.Internal
	rec.Attachable = p.Attachable
	rec.EnableIPv6 = p.EnableIPv6
	rec.Status = NetworkStatusPresent
	rec.ErrMsg = ""
	rec.UpdatedAt = now

	s.records[p.DockerID] = rec

	s.log.Info("network registered: id=%s name=%q driver=%s origin=%s",
		rec.ShortID, p.Name, p.Driver, p.Origin)
	return rec.clone()
}

// MarkDeleted transitions the network to NetworkStatusDeleted (in-band removal).
// Returns ErrNetworkNotFound if the DockerID is not in the store.
func (s *NetworkStore) MarkDeleted(dockerID string) error {
	return s.transition(dockerID, NetworkStatusDeleted, "")
}

// MarkDeletedOutOfBand transitions the network to NetworkStatusDeletedOutOfBand.
// Call this when Reconcile detects the network vanished without going through
// dokoko.  Returns ErrNetworkNotFound if the DockerID is unknown.
func (s *NetworkStore) MarkDeletedOutOfBand(dockerID string) error {
	return s.transition(dockerID, NetworkStatusDeletedOutOfBand, "")
}

// MarkErrored records an error against the network.
// Returns ErrNetworkNotFound if the DockerID is unknown.
func (s *NetworkStore) MarkErrored(dockerID, errMsg string) error {
	if errMsg == "" {
		errMsg = "(no error message provided)"
	}
	return s.transition(dockerID, NetworkStatusErrored, errMsg)
}

// MarkPresent transitions a previously-deleted or errored network back to
// present.  Returns ErrNetworkNotFound if the DockerID is unknown.
func (s *NetworkStore) MarkPresent(dockerID string) error {
	return s.transition(dockerID, NetworkStatusPresent, "")
}

// transition updates Status + ErrMsg atomically.
func (s *NetworkStore) transition(dockerID string, status NetworkStatus, errMsg string) error {
	s.log.LowTrace("transition: id=%s → %s", shortNetID(dockerID), status)

	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.records[dockerID]
	if !ok {
		s.log.Warn("transition: network %q not found", shortNetID(dockerID))
		return fmt.Errorf("%w: %s", ErrNetworkNotFound, dockerID)
	}

	prev := rec.Status
	rec.Status = status
	rec.ErrMsg = errMsg
	rec.UpdatedAt = time.Now()

	s.log.Debug("transition: %s %s → %s", rec.ShortID, prev, status)
	if errMsg != "" {
		s.log.Warn("network errored: id=%s err=%s", rec.ShortID, errMsg)
	} else {
		s.log.Info("network status changed: id=%s name=%q %s → %s",
			rec.ShortID, rec.Name, prev, status)
	}
	return nil
}

// ── Queries ───────────────────────────────────────────────────────────────────

// Get returns a snapshot copy of the record for dockerID.
// Returns false if the network is not in the store.
func (s *NetworkStore) Get(dockerID string) (*NetworkRecord, bool) {
	s.log.Trace("Get: %s", shortNetID(dockerID))
	s.mu.RLock()
	rec, ok := s.records[dockerID]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return rec.clone(), true
}

// All returns snapshot copies of every record in the store.
func (s *NetworkStore) All() []*NetworkRecord {
	s.log.Trace("All: reading network store")
	s.mu.RLock()
	out := make([]*NetworkRecord, 0, len(s.records))
	for _, r := range s.records {
		out = append(out, r.clone())
	}
	s.mu.RUnlock()
	s.log.Debug("All: returned %d records", len(out))
	return out
}

// ByStatus returns snapshot copies of all records whose Status matches.
func (s *NetworkStore) ByStatus(status NetworkStatus) []*NetworkRecord {
	s.log.Trace("ByStatus: %s", status)
	s.mu.RLock()
	var out []*NetworkRecord
	for _, r := range s.records {
		if r.Status == status {
			out = append(out, r.clone())
		}
	}
	s.mu.RUnlock()
	s.log.Debug("ByStatus %s: %d records", status, len(out))
	return out
}

// Size returns the number of records in the store.
func (s *NetworkStore) Size() int {
	s.mu.RLock()
	n := len(s.records)
	s.mu.RUnlock()
	return n
}

// ── Reconcile ─────────────────────────────────────────────────────────────────

// Reconcile compares a snapshot of currently-live Docker network IDs against
// the store and marks any Present network that is absent from liveIDs as
// NetworkStatusDeletedOutOfBand.
//
// It returns the DockerIDs of every record that was transitioned.  Call this
// after a fresh ops.List to keep the store consistent with Docker's reality.
func (s *NetworkStore) Reconcile(liveIDs []string) []string {
	s.log.LowTrace("Reconcile: %d live IDs provided", len(liveIDs))

	live := make(map[string]struct{}, len(liveIDs))
	for _, id := range liveIDs {
		live[id] = struct{}{}
	}

	s.mu.Lock()
	var affected []string
	for id, rec := range s.records {
		if rec.Status != NetworkStatusPresent {
			continue
		}
		if _, found := live[id]; !found {
			prev := rec.Status
			rec.Status = NetworkStatusDeletedOutOfBand
			rec.UpdatedAt = time.Now()
			affected = append(affected, id)
			s.log.Warn("reconcile: out-of-band deletion detected: id=%s name=%q prev=%s",
				rec.ShortID, rec.Name, prev)
		}
	}
	s.mu.Unlock()

	s.log.Info("reconcile complete: %d/%d networks marked deleted_out_of_band",
		len(affected), len(s.records))
	return affected
}

// ── Internal helpers ──────────────────────────────────────────────────────────

func shortNetID(id string) string {
	s := strings.TrimPrefix(id, "sha256:")
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
