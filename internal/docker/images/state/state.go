// Package dockerimagestate manages the in-memory lifecycle of Docker image
// state change requests for this program.
//
// Lifecycle:
//
//	RequestChange() ──► [requested]
//	                        │
//	           ┌────────────┼────────────┐
//	           ▼            ▼            ▼
//	    ConfirmSuccess  RecordFailure  Abandon
//	         │                │            │
//	      [active]         [failed]   [abandoned]
//
// The actor module drives changes from [requested] into one of the three
// terminal buckets.  The state module is only responsible for bookkeeping;
// it never calls ops itself.
package dockerimagestate

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"dokoko.ai/dokoko/pkg/logger"
)

// ── Operation ────────────────────────────────────────────────────────────────

// Operation is the kind of image action being requested.
type Operation string

const (
	OpPull   Operation = "pull"
	OpRemove Operation = "remove"
	OpTag    Operation = "tag"
)

// ── Core records ─────────────────────────────────────────────────────────────

// StateChange is created by RequestChange and describes what should happen.
// It is immutable once created; callers must not modify it.
type StateChange struct {
	ID          string            // unique, sortable: "chg-<nano>-<seq>"
	Op          Operation         // what to do
	ImageRef    string            // e.g. "busybox:latest"
	Meta        map[string]string // optional: platform, target tag for Tag, etc.
	RequestedAt time.Time
}

// ActiveRecord is written by ConfirmSuccess and represents a change the ops
// layer confirmed was applied.
type ActiveRecord struct {
	Change      *StateChange
	DockerID    string // image ID returned by the daemon (empty for removes)
	ConfirmedAt time.Time
}

// FailedRecord is written by RecordFailure and preserves the full error
// context alongside the original request.
type FailedRecord struct {
	Change   *StateChange
	Err      string // human-readable error from ops
	FailedAt time.Time
}

// AbandonedRecord is written by Abandon and records why a requested change
// was never executed.
type AbandonedRecord struct {
	Change      *StateChange
	Reason      string
	AbandonedAt time.Time
}

// ── State ────────────────────────────────────────────────────────────────────

// State is the single source of truth for image state changes in this process.
// All exported methods are safe for concurrent use.
type State struct {
	mu        sync.RWMutex
	requested []*StateChange
	active    []*ActiveRecord
	failed    []*FailedRecord
	abandoned []*AbandonedRecord

	seq uint64 // monotonic counter for unique IDs (accessed via atomic)
	log *logger.Logger
}

// New returns an empty, ready-to-use State.
func New(log *logger.Logger) *State {
	log.LowTrace("initialising image state store")
	s := &State{log: log}
	log.Debug("state store allocated (requested=0 active=0 failed=0 abandoned=0)")
	log.Info("image state store ready")
	return s
}

// ── Mutations ────────────────────────────────────────────────────────────────

// RequestChange registers a new desired state change and places it in the
// requested bucket.  The caller (actor) will later drive it to one of the
// three terminal buckets.
func (s *State) RequestChange(op Operation, imageRef string, meta map[string]string) *StateChange {
	s.log.LowTrace("requesting state change: op=%s ref=%s", op, imageRef)
	s.log.Trace("meta keys: %d", len(meta))
	for k, v := range meta {
		s.log.Trace("  meta[%s]=%s", k, v)
	}

	seq := atomic.AddUint64(&s.seq, 1)
	id := fmt.Sprintf("chg-%d-%06d", time.Now().UnixNano(), seq)
	s.log.Debug("generated change ID: %s", id)

	// Copy meta so the caller cannot mutate it after returning.
	metaCopy := make(map[string]string, len(meta))
	for k, v := range meta {
		metaCopy[k] = v
	}

	change := &StateChange{
		ID:          id,
		Op:          op,
		ImageRef:    imageRef,
		Meta:        metaCopy,
		RequestedAt: time.Now(),
	}

	s.mu.Lock()
	s.requested = append(s.requested, change)
	n := len(s.requested)
	s.mu.Unlock()

	s.log.Debug("change %s added to requested (requested total=%d)", id, n)
	s.log.Info("state change requested: id=%s op=%s ref=%s", id, op, imageRef)
	return change
}

// ConfirmSuccess moves the change identified by changeID from the requested
// bucket into the active bucket.  dockerID is the image ID reported by the
// daemon (may be empty for remove operations).
//
// Returns ErrChangeNotFound if no requested change with that ID exists.
func (s *State) ConfirmSuccess(changeID, dockerID string) (*ActiveRecord, error) {
	s.log.LowTrace("confirming success: id=%s dockerID=%s", changeID, dockerID)

	s.mu.Lock()
	defer s.mu.Unlock()

	idx, change := s.findRequested(changeID)
	if change == nil {
		s.log.Error("ConfirmSuccess: change %q not found in requested bucket", changeID)
		return nil, fmt.Errorf("%w: %s", ErrChangeNotFound, changeID)
	}

	s.log.Debug("found change at requested[%d]: op=%s ref=%s", idx, change.Op, change.ImageRef)

	s.requested = removeAt(s.requested, idx)
	s.log.Trace("removed change from requested (remaining=%d)", len(s.requested))

	rec := &ActiveRecord{
		Change:      change,
		DockerID:    dockerID,
		ConfirmedAt: time.Now(),
	}
	s.active = append(s.active, rec)

	s.log.Debug("change %s moved to active (active total=%d)", changeID, len(s.active))
	s.log.Info("state change confirmed active: id=%s op=%s ref=%s dockerID=%s",
		changeID, change.Op, change.ImageRef, dockerID)
	return rec, nil
}

// RecordFailure moves the change identified by changeID from the requested
// bucket into the failed bucket, preserving the error for diagnostics.
//
// Returns ErrChangeNotFound if no requested change with that ID exists.
func (s *State) RecordFailure(changeID string, err error) (*FailedRecord, error) {
	s.log.LowTrace("recording failure: id=%s err=%v", changeID, err)

	if err == nil {
		s.log.Warn("RecordFailure called with nil error for change %s – using placeholder", changeID)
		err = errors.New("(no error provided)")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	idx, change := s.findRequested(changeID)
	if change == nil {
		s.log.Error("RecordFailure: change %q not found in requested bucket", changeID)
		return nil, fmt.Errorf("%w: %s", ErrChangeNotFound, changeID)
	}

	s.log.Debug("found change at requested[%d]: op=%s ref=%s", idx, change.Op, change.ImageRef)

	s.requested = removeAt(s.requested, idx)
	s.log.Trace("removed change from requested (remaining=%d)", len(s.requested))

	rec := &FailedRecord{
		Change:   change,
		Err:      err.Error(),
		FailedAt: time.Now(),
	}
	s.failed = append(s.failed, rec)

	s.log.Debug("change %s moved to failed (failed total=%d)", changeID, len(s.failed))
	s.log.Warn("state change failed: id=%s op=%s ref=%s err=%s",
		changeID, change.Op, change.ImageRef, err)
	return rec, nil
}

// Abandon moves the change identified by changeID from the requested bucket
// into the abandoned bucket with the supplied reason.
//
// Returns ErrChangeNotFound if no requested change with that ID exists.
func (s *State) Abandon(changeID, reason string) (*AbandonedRecord, error) {
	s.log.LowTrace("abandoning change: id=%s reason=%q", changeID, reason)

	if reason == "" {
		reason = "(no reason given)"
		s.log.Warn("Abandon called with empty reason for change %s", changeID)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	idx, change := s.findRequested(changeID)
	if change == nil {
		s.log.Error("Abandon: change %q not found in requested bucket", changeID)
		return nil, fmt.Errorf("%w: %s", ErrChangeNotFound, changeID)
	}

	s.log.Debug("found change at requested[%d]: op=%s ref=%s", idx, change.Op, change.ImageRef)

	s.requested = removeAt(s.requested, idx)
	s.log.Trace("removed change from requested (remaining=%d)", len(s.requested))

	rec := &AbandonedRecord{
		Change:      change,
		Reason:      reason,
		AbandonedAt: time.Now(),
	}
	s.abandoned = append(s.abandoned, rec)

	s.log.Debug("change %s moved to abandoned (abandoned total=%d)", changeID, len(s.abandoned))
	s.log.Warn("state change abandoned: id=%s op=%s ref=%s reason=%q",
		changeID, change.Op, change.ImageRef, reason)
	return rec, nil
}

// ── Queries ──────────────────────────────────────────────────────────────────

// Requested returns a snapshot of all pending state changes.
// The slice is a copy; modifications do not affect internal state.
func (s *State) Requested() []*StateChange {
	s.log.Trace("reading requested bucket")
	s.mu.RLock()
	out := copySlice(s.requested)
	s.mu.RUnlock()
	s.log.Debug("requested snapshot: %d items", len(out))
	return out
}

// Active returns a snapshot of all successfully confirmed changes.
func (s *State) Active() []*ActiveRecord {
	s.log.Trace("reading active bucket")
	s.mu.RLock()
	out := copySlice(s.active)
	s.mu.RUnlock()
	s.log.Debug("active snapshot: %d items", len(out))
	return out
}

// Failed returns a snapshot of all failed change attempts.
func (s *State) Failed() []*FailedRecord {
	s.log.Trace("reading failed bucket")
	s.mu.RLock()
	out := copySlice(s.failed)
	s.mu.RUnlock()
	s.log.Debug("failed snapshot: %d items", len(out))
	return out
}

// Abandoned returns a snapshot of all abandoned changes.
func (s *State) Abandoned() []*AbandonedRecord {
	s.log.Trace("reading abandoned bucket")
	s.mu.RLock()
	out := copySlice(s.abandoned)
	s.mu.RUnlock()
	s.log.Debug("abandoned snapshot: %d items", len(out))
	return out
}

// Summary returns live counts for all four buckets.
func (s *State) Summary() (requested, active, failed, abandoned int) {
	s.log.Trace("reading state summary")
	s.mu.RLock()
	requested = len(s.requested)
	active = len(s.active)
	failed = len(s.failed)
	abandoned = len(s.abandoned)
	s.mu.RUnlock()
	s.log.Debug("summary: requested=%d active=%d failed=%d abandoned=%d",
		requested, active, failed, abandoned)
	return
}

// FindByID searches all buckets for a change with the given ID and returns its
// current status alongside the raw record (one of *StateChange, *ActiveRecord,
// *FailedRecord, or *AbandonedRecord).  Returns ErrChangeNotFound if absent.
func (s *State) FindByID(changeID string) (status Status, record any, err error) {
	s.log.Trace("FindByID: searching all buckets for %s", changeID)
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, c := range s.requested {
		if c.ID == changeID {
			s.log.Debug("FindByID %s: found in requested", changeID)
			return StatusRequested, c, nil
		}
	}
	for _, r := range s.active {
		if r.Change.ID == changeID {
			s.log.Debug("FindByID %s: found in active", changeID)
			return StatusActive, r, nil
		}
	}
	for _, r := range s.failed {
		if r.Change.ID == changeID {
			s.log.Debug("FindByID %s: found in failed", changeID)
			return StatusFailed, r, nil
		}
	}
	for _, r := range s.abandoned {
		if r.Change.ID == changeID {
			s.log.Debug("FindByID %s: found in abandoned", changeID)
			return StatusAbandoned, r, nil
		}
	}

	s.log.Warn("FindByID %s: not found in any bucket", changeID)
	return "", nil, fmt.Errorf("%w: %s", ErrChangeNotFound, changeID)
}

// ── Status ───────────────────────────────────────────────────────────────────

// Status represents which bucket a change currently lives in.
type Status string

const (
	StatusRequested  Status = "requested"
	StatusActive     Status = "active"
	StatusFailed     Status = "failed"
	StatusAbandoned  Status = "abandoned"
)

// ── Errors ───────────────────────────────────────────────────────────────────

// ErrChangeNotFound is returned when a changeID is not present in the
// requested bucket (it may have already been resolved or never existed).
var ErrChangeNotFound = errors.New("state change not found")

// ── Internal helpers ─────────────────────────────────────────────────────────

// findRequested scans the requested slice for changeID.
// Must be called with s.mu held (read or write).
func (s *State) findRequested(changeID string) (int, *StateChange) {
	for i, c := range s.requested {
		if c.ID == changeID {
			return i, c
		}
	}
	return -1, nil
}

// removeAt removes the element at index i from a []*StateChange without
// preserving order (O(1) swap-and-shrink).
func removeAt(sl []*StateChange, i int) []*StateChange {
	last := len(sl) - 1
	sl[i] = sl[last]
	sl[last] = nil
	return sl[:last]
}

// copySlice returns a shallow copy of a slice so callers get a snapshot.
func copySlice[T any](src []T) []T {
	if len(src) == 0 {
		return nil
	}
	out := make([]T, len(src))
	copy(out, src)
	return out
}
