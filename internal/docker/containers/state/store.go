// store.go — in-process container inventory with lifecycle and origin tracking.
//
// The Store tracks every container this process has ever observed, keyed by
// the Docker-assigned container ID (a stable, content-addressed identifier).
//
// Two independent axes are tracked per container:
//
//   - ContainerStatus: whether the container exists in our inventory
//     (present / removed / removed_out_of_band / errored)
//
//   - RuntimeState: Docker's current runtime state for the container
//     ("running", "exited", "paused", "dead", "created", etc.)
//
// Lifecycle diagram (ContainerStatus):
//
//	Register()
//	  ──► [present]
//	           │
//	    ┌──────┼──────────────────────┐
//	    ▼      ▼                      ▼
//	[removed] [removed_out_of_band] [errored]
//	    │                              │
//	    └──────────────────────────────┘
//	                  │
//	               Register()   ← re-create restores to present
//	                  ▼
//	              [present]

package dockercontainerstate

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"dokoko.ai/dokoko/pkg/logger"
)

// ── Errors ────────────────────────────────────────────────────────────────────

// ErrContainerNotFound is returned by Mark* / UpdateRuntimeState when the
// DockerID is absent from the store.
var ErrContainerNotFound = errors.New("container not found in store")

// ── ContainerOrigin ───────────────────────────────────────────────────────────

// ContainerOrigin describes how a container entered the store.
type ContainerOrigin string

const (
	// ContainerOriginInBand means the container was created through dokoko.
	ContainerOriginInBand ContainerOrigin = "in_band"

	// ContainerOriginOutOfBand means the container was already present in
	// Docker when first observed, or appeared without going through dokoko.
	ContainerOriginOutOfBand ContainerOrigin = "out_of_band"
)

// ── ContainerInventoryStatus ──────────────────────────────────────────────────

// ContainerInventoryStatus describes whether the container exists in the store's
// inventory (independent of Docker's runtime state for the container).
type ContainerInventoryStatus string

const (
	// ContainerStatusPresent means the container is confirmed to exist in Docker.
	ContainerStatusPresent ContainerInventoryStatus = "present"

	// ContainerStatusRemoved means the container was removed through dokoko.
	ContainerStatusRemoved ContainerInventoryStatus = "removed"

	// ContainerStatusRemovedOutOfBand means the container disappeared from Docker
	// without going through dokoko, detected during Reconcile.
	ContainerStatusRemovedOutOfBand ContainerInventoryStatus = "removed_out_of_band"

	// ContainerStatusErrored means an operation on this container resulted in
	// an error.  ErrMsg on the record holds the details.
	ContainerStatusErrored ContainerInventoryStatus = "errored"
)

// ── RegisterContainerParams ───────────────────────────────────────────────────

// RegisterContainerParams carries all metadata needed to upsert a container
// record.  Populate it from dockertypes.ContainerJSON or dockertypes.Container
// at the call site.
type RegisterContainerParams struct {
	DockerID     string          // full Docker container ID
	Name         string          // primary name (leading "/" stripped)
	Image        string          // image name/tag used to create the container
	ImageID      string          // sha256 of the image
	RuntimeState string          // Docker's runtime state: "running", "exited", etc.
	ExitCode     int             // last exit code (0 if still running)
	NetworkMode  string          // e.g. "bridge", "host", "none"
	CreatedAt    time.Time       // when Docker created the container
	Origin       ContainerOrigin // InBand or OutOfBand
}

// ── ContainerRecord ───────────────────────────────────────────────────────────

// ContainerRecord is the store's complete view of one tracked container.
// Callers receive copies; the internal pointer is never exposed.
type ContainerRecord struct {
	// Identity
	DockerID string // full Docker container ID
	ShortID  string // first 12 hex chars (for display)
	Name     string // primary container name

	// Docker metadata
	Image       string // image name/tag
	ImageID     string // image sha256
	NetworkMode string // network mode

	// Runtime (mutable — updated by UpdateRuntimeState)
	RuntimeState string // Docker's state string
	ExitCode     int    // last exit code

	// Timestamps
	CreatedAt    time.Time // when Docker created the container
	RegisteredAt time.Time // first time this store observed the container
	UpdatedAt    time.Time // last mutation of any field

	// Lifecycle
	Origin ContainerOrigin          // how this container entered the store
	Status ContainerInventoryStatus // current inventory state
	ErrMsg string                   // non-empty only when Status == ContainerStatusErrored
}

// clone returns a safe snapshot of the record.
func (r *ContainerRecord) clone() *ContainerRecord {
	cp := *r
	return &cp
}

// ── ContainerStore ────────────────────────────────────────────────────────────

// ContainerStore is an in-memory registry of all containers known to this
// process.  All exported methods are safe for concurrent use.
type ContainerStore struct {
	mu      sync.RWMutex
	records map[string]*ContainerRecord // keyed by DockerID
	log     *logger.Logger
}

// NewContainerStore returns an empty, ready-to-use ContainerStore.
func NewContainerStore(log *logger.Logger) *ContainerStore {
	log.LowTrace("initialising container store")
	s := &ContainerStore{
		records: make(map[string]*ContainerRecord),
		log:     log,
	}
	log.Info("container store ready")
	return s
}

// ── Mutations ─────────────────────────────────────────────────────────────────

// Register upserts a container record.
//
// First call: creates a new record with Status=Present.
// Subsequent calls: updates mutable fields and sets Status back to Present.
// Origin is upgraded from OutOfBand to InBand; never downgraded.
func (s *ContainerStore) Register(p RegisterContainerParams) *ContainerRecord {
	s.log.LowTrace("register: dockerID=%s name=%q origin=%s",
		shortContainerID(p.DockerID), p.Name, p.Origin)

	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	rec, exists := s.records[p.DockerID]

	if !exists {
		rec = &ContainerRecord{
			DockerID:     p.DockerID,
			ShortID:      shortContainerID(p.DockerID),
			RegisteredAt: now,
			CreatedAt:    p.CreatedAt,
			Origin:       p.Origin,
		}
		s.log.Debug("register: new container record: id=%s name=%q origin=%s",
			rec.ShortID, p.Name, p.Origin)
	} else {
		if p.Origin == ContainerOriginInBand && rec.Origin == ContainerOriginOutOfBand {
			s.log.Debug("register: upgrading origin OutOfBand→InBand for %s", rec.ShortID)
			rec.Origin = ContainerOriginInBand
		}
		s.log.Debug("register: updating container record: id=%s prev_status=%s",
			rec.ShortID, rec.Status)
	}

	// Always refresh mutable fields.
	rec.Name = p.Name
	rec.Image = p.Image
	rec.ImageID = p.ImageID
	rec.RuntimeState = p.RuntimeState
	rec.ExitCode = p.ExitCode
	rec.NetworkMode = p.NetworkMode
	rec.Status = ContainerStatusPresent
	rec.ErrMsg = ""
	rec.UpdatedAt = now

	s.records[p.DockerID] = rec

	s.log.Info("container registered: id=%s name=%q image=%s runtime=%s origin=%s",
		rec.ShortID, p.Name, p.Image, p.RuntimeState, p.Origin)
	return rec.clone()
}

// UpdateRuntimeState updates the RuntimeState and ExitCode fields for an
// existing record without changing the inventory status.  Call this when a
// polling loop or event stream reports a state change (e.g. container stopped).
//
// Returns ErrContainerNotFound if the DockerID is not in the store.
func (s *ContainerStore) UpdateRuntimeState(dockerID, runtimeState string, exitCode int) error {
	s.log.LowTrace("UpdateRuntimeState: id=%s → %s", shortContainerID(dockerID), runtimeState)

	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.records[dockerID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrContainerNotFound, dockerID)
	}

	prev := rec.RuntimeState
	rec.RuntimeState = runtimeState
	rec.ExitCode = exitCode
	rec.UpdatedAt = time.Now()

	s.log.Debug("runtime state: %s %q → %q (exit=%d)", rec.ShortID, prev, runtimeState, exitCode)
	return nil
}

// MarkRemoved transitions the container to ContainerStatusRemoved (in-band).
// Returns ErrContainerNotFound if the DockerID is not in the store.
func (s *ContainerStore) MarkRemoved(dockerID string) error {
	return s.transition(dockerID, ContainerStatusRemoved, "")
}

// MarkRemovedOutOfBand transitions the container to ContainerStatusRemovedOutOfBand.
// Call this when Reconcile detects the container vanished without going through
// dokoko.  Returns ErrContainerNotFound if the DockerID is unknown.
func (s *ContainerStore) MarkRemovedOutOfBand(dockerID string) error {
	return s.transition(dockerID, ContainerStatusRemovedOutOfBand, "")
}

// MarkErrored records an error against the container.
// Returns ErrContainerNotFound if the DockerID is unknown.
func (s *ContainerStore) MarkErrored(dockerID, errMsg string) error {
	if errMsg == "" {
		errMsg = "(no error message provided)"
	}
	return s.transition(dockerID, ContainerStatusErrored, errMsg)
}

// MarkPresent transitions a previously-removed or errored container back to
// present.  Returns ErrContainerNotFound if the DockerID is unknown.
func (s *ContainerStore) MarkPresent(dockerID string) error {
	return s.transition(dockerID, ContainerStatusPresent, "")
}

// transition updates Status + ErrMsg atomically.
func (s *ContainerStore) transition(dockerID string, status ContainerInventoryStatus, errMsg string) error {
	s.log.LowTrace("transition: id=%s → %s", shortContainerID(dockerID), status)

	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.records[dockerID]
	if !ok {
		s.log.Warn("transition: container %q not found", shortContainerID(dockerID))
		return fmt.Errorf("%w: %s", ErrContainerNotFound, dockerID)
	}

	prev := rec.Status
	rec.Status = status
	rec.ErrMsg = errMsg
	rec.UpdatedAt = time.Now()

	s.log.Debug("transition: %s %s → %s", rec.ShortID, prev, status)
	if errMsg != "" {
		s.log.Warn("container errored: id=%s err=%s", rec.ShortID, errMsg)
	} else {
		s.log.Info("container status changed: id=%s name=%q %s → %s",
			rec.ShortID, rec.Name, prev, status)
	}
	return nil
}

// ── Queries ───────────────────────────────────────────────────────────────────

// Get returns a snapshot copy of the record for dockerID.
// Returns false if the container is not in the store.
func (s *ContainerStore) Get(dockerID string) (*ContainerRecord, bool) {
	s.log.Trace("Get: %s", shortContainerID(dockerID))
	s.mu.RLock()
	rec, ok := s.records[dockerID]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return rec.clone(), true
}

// All returns snapshot copies of every record in the store.
func (s *ContainerStore) All() []*ContainerRecord {
	s.log.Trace("All: reading container store")
	s.mu.RLock()
	out := make([]*ContainerRecord, 0, len(s.records))
	for _, r := range s.records {
		out = append(out, r.clone())
	}
	s.mu.RUnlock()
	s.log.Debug("All: returned %d records", len(out))
	return out
}

// ByStatus returns snapshot copies of all records whose inventory Status matches.
func (s *ContainerStore) ByStatus(status ContainerInventoryStatus) []*ContainerRecord {
	s.log.Trace("ByStatus: %s", status)
	s.mu.RLock()
	var out []*ContainerRecord
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
func (s *ContainerStore) Size() int {
	s.mu.RLock()
	n := len(s.records)
	s.mu.RUnlock()
	return n
}

// ── Reconcile ─────────────────────────────────────────────────────────────────

// Reconcile compares a snapshot of currently-live Docker container IDs against
// the store and marks any Present container that is absent from liveIDs as
// ContainerStatusRemovedOutOfBand.
//
// It returns the DockerIDs of every record that was transitioned.  Call this
// after a fresh ops.List (docker ps -a equivalent) to keep the store consistent
// with Docker's reality.
func (s *ContainerStore) Reconcile(liveIDs []string) []string {
	s.log.LowTrace("Reconcile: %d live IDs provided", len(liveIDs))

	live := make(map[string]struct{}, len(liveIDs))
	for _, id := range liveIDs {
		live[id] = struct{}{}
	}

	s.mu.Lock()
	var affected []string
	for id, rec := range s.records {
		if rec.Status != ContainerStatusPresent {
			continue
		}
		if _, found := live[id]; !found {
			prev := rec.Status
			rec.Status = ContainerStatusRemovedOutOfBand
			rec.UpdatedAt = time.Now()
			affected = append(affected, id)
			s.log.Warn("reconcile: out-of-band removal detected: id=%s name=%q prev=%s",
				rec.ShortID, rec.Name, prev)
		}
	}
	s.mu.Unlock()

	s.log.Info("reconcile complete: %d/%d containers marked removed_out_of_band",
		len(affected), len(s.records))
	return affected
}

// ── Internal helpers ──────────────────────────────────────────────────────────

func shortContainerID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
