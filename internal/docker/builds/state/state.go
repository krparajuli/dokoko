// Package dockerbuildstate manages the in-memory lifecycle of Docker image
// build state change requests for this program.
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
package dockerbuildstate

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"dokoko.ai/dokoko/pkg/logger"
)

// ── Operation ─────────────────────────────────────────────────────────────────

// Operation is the kind of build action being requested.
type Operation string

const (
	OpBuild      Operation = "build"
	OpPruneCache Operation = "prune-cache"
)

// ── Core records ──────────────────────────────────────────────────────────────

// StateChange is created by RequestChange and describes what should happen.
// It is immutable once created; callers must not modify it.
type StateChange struct {
	ID          string            // unique, sortable: "bchg-<nano>-<seq>"
	Op          Operation         // what to do
	Tags        string            // comma-joined target tags; empty for prune-cache
	Meta        map[string]string // optional: dockerfile, target, platform, etc.
	RequestedAt time.Time
}

// ActiveRecord is written by ConfirmSuccess and represents a change the ops
// layer confirmed was applied.
type ActiveRecord struct {
	Change      *StateChange
	ImageID     string // image SHA256 returned by the daemon (empty for prune-cache)
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

// ── State ─────────────────────────────────────────────────────────────────────

// State is the single source of truth for build state changes in this process.
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
	log.LowTrace("initialising build state store")
	s := &State{log: log}
	log.Debug("state store allocated (requested=0 active=0 failed=0 abandoned=0)")
	log.Info("build state store ready")
	return s
}

// ── Mutations ─────────────────────────────────────────────────────────────────

// RequestChange registers a new desired state change and places it in the
// requested bucket.  The caller (actor) will later drive it to one of the
// three terminal buckets.
func (s *State) RequestChange(op Operation, tags string, meta map[string]string) *StateChange {
	s.log.LowTrace("requesting state change: op=%s tags=%q", op, tags)
	s.log.Trace("meta keys: %d", len(meta))

	seq := atomic.AddUint64(&s.seq, 1)
	id := fmt.Sprintf("bchg-%d-%06d", time.Now().UnixNano(), seq)
	s.log.Debug("generated change ID: %s", id)

	metaCopy := make(map[string]string, len(meta))
	for k, v := range meta {
		metaCopy[k] = v
	}

	c := &StateChange{
		ID:          id,
		Op:          op,
		Tags:        tags,
		Meta:        metaCopy,
		RequestedAt: time.Now(),
	}

	s.mu.Lock()
	s.requested = append(s.requested, c)
	n := len(s.requested)
	s.mu.Unlock()

	s.log.Debug("change registered: id=%s op=%s tags=%q (requested=%d)",
		id, op, tags, n)
	s.log.Info("build change requested: %s (%s)", op, tags)
	return c
}

// ConfirmSuccess moves a change from requested → active and records the
// image ID returned by the daemon (empty for prune-cache).
func (s *State) ConfirmSuccess(changeID, imageID string) (*ActiveRecord, error) {
	s.log.LowTrace("confirming success: changeID=%s imageID=%s", changeID, shortID(imageID))

	s.mu.Lock()
	defer s.mu.Unlock()

	idx, c := s.findRequested(changeID)
	if c == nil {
		s.log.Error("ConfirmSuccess: change %q not found in requested bucket", changeID)
		return nil, ErrChangeNotFound
	}
	s.requested = append(s.requested[:idx], s.requested[idx+1:]...)

	rec := &ActiveRecord{
		Change:      c,
		ImageID:     imageID,
		ConfirmedAt: time.Now(),
	}
	s.active = append(s.active, rec)

	s.log.Debug("change %s confirmed active (imageID=%s)", changeID, shortID(imageID))
	s.log.Info("build change active: %s (%s) → imageID=%s", c.Op, c.Tags, shortID(imageID))
	return rec, nil
}

// RecordFailure moves a change from requested → failed.
func (s *State) RecordFailure(changeID string, opErr error) (*FailedRecord, error) {
	s.log.LowTrace("recording failure: changeID=%s", changeID)

	errStr := "(unknown error)"
	if opErr != nil {
		errStr = opErr.Error()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	idx, c := s.findRequested(changeID)
	if c == nil {
		s.log.Error("RecordFailure: change %q not found in requested bucket", changeID)
		return nil, ErrChangeNotFound
	}
	s.requested = append(s.requested[:idx], s.requested[idx+1:]...)

	rec := &FailedRecord{
		Change:   c,
		Err:      errStr,
		FailedAt: time.Now(),
	}
	s.failed = append(s.failed, rec)

	s.log.Debug("change %s recorded as failed: %s", changeID, errStr)
	s.log.Warn("build change failed: %s (%s): %s", c.Op, c.Tags, errStr)
	return rec, nil
}

// Abandon moves a change from requested → abandoned.
func (s *State) Abandon(changeID, reason string) (*AbandonedRecord, error) {
	s.log.LowTrace("abandoning change: changeID=%s reason=%q", changeID, reason)

	if reason == "" {
		reason = "(no reason provided)"
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	idx, c := s.findRequested(changeID)
	if c == nil {
		s.log.Error("Abandon: change %q not found in requested bucket", changeID)
		return nil, ErrChangeNotFound
	}
	s.requested = append(s.requested[:idx], s.requested[idx+1:]...)

	rec := &AbandonedRecord{
		Change:      c,
		Reason:      reason,
		AbandonedAt: time.Now(),
	}
	s.abandoned = append(s.abandoned, rec)

	s.log.Debug("change %s abandoned: %s", changeID, reason)
	s.log.Warn("build change abandoned: %s (%s): %s", c.Op, c.Tags, reason)
	return rec, nil
}

// ── Read-only snapshots ────────────────────────────────────────────────────────

// Requested returns a snapshot of all pending changes.
func (s *State) Requested() []*StateChange {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return copySlice(s.requested)
}

// Active returns a snapshot of all confirmed-active changes.
func (s *State) Active() []*ActiveRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return copySlice(s.active)
}

// Failed returns a snapshot of all failed changes.
func (s *State) Failed() []*FailedRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return copySlice(s.failed)
}

// Abandoned returns a snapshot of all abandoned changes.
func (s *State) Abandoned() []*AbandonedRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return copySlice(s.abandoned)
}

// Summary returns the count of changes in each bucket.
func (s *State) Summary() (requested, active, failed, abandoned int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.requested), len(s.active), len(s.failed), len(s.abandoned)
}

// ── FindByID ──────────────────────────────────────────────────────────────────

// Status represents which lifecycle bucket a change is currently in.
type Status string

const (
	StatusRequested Status = "requested"
	StatusActive    Status = "active"
	StatusFailed    Status = "failed"
	StatusAbandoned Status = "abandoned"
)

// FindByID locates a change across all buckets and returns its current status
// and the corresponding record.
func (s *State) FindByID(changeID string) (Status, any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, c := range s.requested {
		if c.ID == changeID {
			return StatusRequested, c, nil
		}
	}
	for _, r := range s.active {
		if r.Change.ID == changeID {
			return StatusActive, r, nil
		}
	}
	for _, r := range s.failed {
		if r.Change.ID == changeID {
			return StatusFailed, r, nil
		}
	}
	for _, r := range s.abandoned {
		if r.Change.ID == changeID {
			return StatusAbandoned, r, nil
		}
	}
	return "", nil, ErrChangeNotFound
}

// ── Errors ────────────────────────────────────────────────────────────────────

// ErrChangeNotFound is returned when a change ID does not exist in any bucket.
var ErrChangeNotFound = errors.New("change not found")

// ── Internal helpers ──────────────────────────────────────────────────────────

// findRequested returns the index and pointer of the change with the given ID
// in the requested slice.  Returns (-1, nil) if not found.
// Must be called with s.mu held (write lock).
func (s *State) findRequested(id string) (int, *StateChange) {
	for i, c := range s.requested {
		if c.ID == id {
			return i, c
		}
	}
	return -1, nil
}

// copySlice returns a shallow copy of a slice so callers cannot mutate the
// internal state through the returned slice header.
func copySlice[T any](src []T) []T {
	out := make([]T, len(src))
	copy(out, src)
	return out
}

// shortID truncates a SHA256 image ID for log messages.
func shortID(id string) string {
	const prefix = "sha256:"
	s := id
	if len(s) > len(prefix) && s[:len(prefix)] == prefix {
		s = s[len(prefix):]
	}
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
