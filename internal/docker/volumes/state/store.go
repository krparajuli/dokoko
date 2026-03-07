// store.go — in-process volume inventory with lifecycle and origin tracking.
//
// The Store tracks every volume this process has ever observed, independently
// of Docker itself.  Volumes are keyed by their name (Docker's stable
// primary identifier for volumes).
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

package dockervolumestate

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"dokoko.ai/dokoko/pkg/logger"
)

// ── Errors ────────────────────────────────────────────────────────────────────

// ErrVolumeNotFound is returned by Mark* operations when the name is absent.
var ErrVolumeNotFound = errors.New("volume not found in store")

// ── Origin ────────────────────────────────────────────────────────────────────

// VolumeOrigin describes how a volume entered the store.
type VolumeOrigin string

const (
	// VolumeOriginInBand means the volume was created through dokoko.
	VolumeOriginInBand VolumeOrigin = "in_band"

	// VolumeOriginOutOfBand means the volume was already present in Docker when
	// first observed, or appeared without going through dokoko.
	VolumeOriginOutOfBand VolumeOrigin = "out_of_band"
)

// ── VolumeStatus ──────────────────────────────────────────────────────────────

// VolumeStatus describes the known lifecycle state of a tracked volume.
type VolumeStatus string

const (
	// VolumeStatusPresent means the volume is confirmed present in Docker.
	VolumeStatusPresent VolumeStatus = "present"

	// VolumeStatusDeleted means the volume was removed through dokoko (in-band).
	VolumeStatusDeleted VolumeStatus = "deleted"

	// VolumeStatusDeletedOutOfBand means the volume disappeared from Docker
	// without going through dokoko, detected during Reconcile.
	VolumeStatusDeletedOutOfBand VolumeStatus = "deleted_out_of_band"

	// VolumeStatusErrored means an operation on this volume resulted in an
	// error.  ErrMsg on the record holds the details.
	VolumeStatusErrored VolumeStatus = "errored"
)

// ── RegisterVolumeParams ──────────────────────────────────────────────────────

// RegisterVolumeParams carries all metadata needed to upsert a volume record.
// Populate it from dockervolume.Volume (or dockervolume.CreateResponse) at the
// call site.
type RegisterVolumeParams struct {
	Name       string            // volume name (Docker's primary identifier)
	Driver     string            // e.g. "local", "nfs"
	Mountpoint string            // e.g. "/var/lib/docker/volumes/mydata/_data"
	Scope      string            // e.g. "local", "global"
	Labels     map[string]string // Docker labels applied to the volume
	Options    map[string]string // driver-specific options
	Origin     VolumeOrigin      // InBand or OutOfBand
}

// ── VolumeRecord ──────────────────────────────────────────────────────────────

// VolumeRecord is the store's complete view of one tracked volume.
// Callers receive copies; the internal pointer is never exposed.
type VolumeRecord struct {
	// Identity
	Name string // volume name (stable, Docker primary key)

	// Docker metadata
	Driver     string            // storage driver
	Mountpoint string            // path on the Docker host
	Scope      string            // "local" or "global"
	Labels     map[string]string // Docker labels
	Options    map[string]string // driver options

	// Timestamps
	RegisteredAt time.Time // first time this store observed the volume
	UpdatedAt    time.Time // last mutation of any field

	// Lifecycle
	Origin VolumeOrigin // how this volume entered the store
	Status VolumeStatus // current known state
	ErrMsg string       // non-empty only when Status == VolumeStatusErrored
}

// clone returns a safe snapshot of the record (deep-copies maps and status).
func (r *VolumeRecord) clone() *VolumeRecord {
	cp := *r
	cp.Labels = copyStringMap(r.Labels)
	cp.Options = copyStringMap(r.Options)
	return &cp
}

// ── VolumeStore ───────────────────────────────────────────────────────────────

// VolumeStore is an in-memory registry of all volumes known to this process.
// All exported methods are safe for concurrent use.
type VolumeStore struct {
	mu      sync.RWMutex
	records map[string]*VolumeRecord // keyed by Name
	log     *logger.Logger
}

// NewVolumeStore returns an empty, ready-to-use VolumeStore.
func NewVolumeStore(log *logger.Logger) *VolumeStore {
	log.LowTrace("initialising volume store")
	s := &VolumeStore{
		records: make(map[string]*VolumeRecord),
		log:     log,
	}
	log.Info("volume store ready")
	return s
}

// ── Mutations ─────────────────────────────────────────────────────────────────

// Register upserts a volume record.
//
// First call: creates a new record with Status=Present.
// Subsequent calls: updates mutable fields and sets Status back to Present.
// Origin is upgraded from OutOfBand to InBand; never downgraded.
func (s *VolumeStore) Register(p RegisterVolumeParams) *VolumeRecord {
	s.log.LowTrace("register: name=%q origin=%s", p.Name, p.Origin)

	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	rec, exists := s.records[p.Name]

	if !exists {
		rec = &VolumeRecord{
			Name:         p.Name,
			RegisteredAt: now,
			Origin:       p.Origin,
		}
		s.log.Debug("register: new volume record: name=%q origin=%s", p.Name, p.Origin)
	} else {
		if p.Origin == VolumeOriginInBand && rec.Origin == VolumeOriginOutOfBand {
			s.log.Debug("register: upgrading origin OutOfBand→InBand for %q", p.Name)
			rec.Origin = VolumeOriginInBand
		}
		s.log.Debug("register: updating volume record: name=%q prev_status=%s", p.Name, rec.Status)
	}

	// Always refresh mutable fields.
	rec.Driver = p.Driver
	rec.Mountpoint = p.Mountpoint
	rec.Scope = p.Scope
	rec.Labels = copyStringMap(p.Labels)
	rec.Options = copyStringMap(p.Options)
	rec.Status = VolumeStatusPresent
	rec.ErrMsg = ""
	rec.UpdatedAt = now

	s.records[p.Name] = rec

	s.log.Info("volume registered: name=%q driver=%s origin=%s", p.Name, p.Driver, p.Origin)
	return rec.clone()
}

// MarkDeleted transitions the volume to VolumeStatusDeleted (in-band removal).
// Returns ErrVolumeNotFound if the name is not in the store.
func (s *VolumeStore) MarkDeleted(name string) error {
	return s.transition(name, VolumeStatusDeleted, "")
}

// MarkDeletedOutOfBand transitions the volume to VolumeStatusDeletedOutOfBand.
// Call this when Reconcile detects the volume vanished without going through
// dokoko.  Returns ErrVolumeNotFound if the name is unknown.
func (s *VolumeStore) MarkDeletedOutOfBand(name string) error {
	return s.transition(name, VolumeStatusDeletedOutOfBand, "")
}

// MarkErrored records an error against the volume.
// Returns ErrVolumeNotFound if the name is unknown.
func (s *VolumeStore) MarkErrored(name, errMsg string) error {
	if errMsg == "" {
		errMsg = "(no error message provided)"
	}
	return s.transition(name, VolumeStatusErrored, errMsg)
}

// MarkPresent transitions a previously-deleted or errored volume back to
// present.  Returns ErrVolumeNotFound if the name is unknown.
func (s *VolumeStore) MarkPresent(name string) error {
	return s.transition(name, VolumeStatusPresent, "")
}

// transition updates Status + ErrMsg atomically.
func (s *VolumeStore) transition(name string, status VolumeStatus, errMsg string) error {
	s.log.LowTrace("transition: name=%q → %s", name, status)

	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.records[name]
	if !ok {
		s.log.Warn("transition: volume %q not found", name)
		return fmt.Errorf("%w: %s", ErrVolumeNotFound, name)
	}

	prev := rec.Status
	rec.Status = status
	rec.ErrMsg = errMsg
	rec.UpdatedAt = time.Now()

	s.log.Debug("transition: %q %s → %s", name, prev, status)
	if errMsg != "" {
		s.log.Warn("volume errored: name=%q err=%s", name, errMsg)
	} else {
		s.log.Info("volume status changed: name=%q %s → %s", name, prev, status)
	}
	return nil
}

// ── Queries ───────────────────────────────────────────────────────────────────

// Get returns a snapshot copy of the record for name.
// Returns false if the volume is not in the store.
func (s *VolumeStore) Get(name string) (*VolumeRecord, bool) {
	s.log.Trace("Get: %q", name)
	s.mu.RLock()
	rec, ok := s.records[name]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return rec.clone(), true
}

// All returns snapshot copies of every record in the store.
func (s *VolumeStore) All() []*VolumeRecord {
	s.log.Trace("All: reading volume store")
	s.mu.RLock()
	out := make([]*VolumeRecord, 0, len(s.records))
	for _, r := range s.records {
		out = append(out, r.clone())
	}
	s.mu.RUnlock()
	s.log.Debug("All: returned %d records", len(out))
	return out
}

// ByStatus returns snapshot copies of all records whose Status matches.
func (s *VolumeStore) ByStatus(status VolumeStatus) []*VolumeRecord {
	s.log.Trace("ByStatus: %s", status)
	s.mu.RLock()
	var out []*VolumeRecord
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
func (s *VolumeStore) Size() int {
	s.mu.RLock()
	n := len(s.records)
	s.mu.RUnlock()
	return n
}

// ── Reconcile ─────────────────────────────────────────────────────────────────

// Reconcile compares a snapshot of currently-live volume names against the
// store and marks any Present volume that is absent from liveNames as
// VolumeStatusDeletedOutOfBand.
//
// It returns the names of every record that was transitioned.  Call this after
// a fresh ops.List to keep the store consistent with Docker's reality.
func (s *VolumeStore) Reconcile(liveNames []string) []string {
	s.log.LowTrace("Reconcile: %d live names provided", len(liveNames))

	live := make(map[string]struct{}, len(liveNames))
	for _, n := range liveNames {
		live[n] = struct{}{}
	}

	s.mu.Lock()
	var affected []string
	for name, rec := range s.records {
		if rec.Status != VolumeStatusPresent {
			continue
		}
		if _, found := live[name]; !found {
			prev := rec.Status
			rec.Status = VolumeStatusDeletedOutOfBand
			rec.UpdatedAt = time.Now()
			affected = append(affected, name)
			s.log.Warn("reconcile: out-of-band deletion detected: name=%q driver=%s prev=%s",
				name, rec.Driver, prev)
		}
	}
	s.mu.Unlock()

	s.log.Info("reconcile complete: %d/%d volumes marked deleted_out_of_band",
		len(affected), len(s.records))
	return affected
}

// ── Internal helpers ──────────────────────────────────────────────────────────

func copyStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
