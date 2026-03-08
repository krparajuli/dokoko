// Package portproxystate manages the in-memory lifecycle of port-proxy
// state change requests.
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
package portproxystate

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"dokoko.ai/dokoko/pkg/logger"
)

// ── Operation ────────────────────────────────────────────────────────────────

// Operation is the kind of port-proxy action being requested.
type Operation string

const (
	OpEnsureProxy         Operation = "ensure_proxy"
	OpRegisterContainer   Operation = "register_container"
	OpDeregisterContainer Operation = "deregister_container"
)

// ── Core records ─────────────────────────────────────────────────────────────

// StateChange is created by RequestChange and describes what should happen.
// It is immutable once created; callers must not modify it.
type StateChange struct {
	ID          string            // unique, sortable: "ppchg-<nano>-<seq>"
	Op          Operation         // what to do
	Ref         string            // container name or "proxy" for EnsureProxy
	Meta        map[string]string // optional metadata
	RequestedAt time.Time
}

// ActiveRecord is written by ConfirmSuccess and represents a change the ops
// layer confirmed was applied.
type ActiveRecord struct {
	Change      *StateChange
	ResultRef   string // e.g. containerID, port count string
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

// State is the single source of truth for port-proxy state changes in this
// process. All exported methods are safe for concurrent use.
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
	log.LowTrace("initialising portproxy state store")
	s := &State{log: log}
	log.Info("portproxy state store ready")
	return s
}

// ── Mutations ────────────────────────────────────────────────────────────────

// RequestChange registers a new desired state change and places it in the
// requested bucket.
func (s *State) RequestChange(op Operation, ref string, meta map[string]string) *StateChange {
	s.log.LowTrace("portproxy: requesting state change: op=%s ref=%s", op, ref)

	seq := atomic.AddUint64(&s.seq, 1)
	id := fmt.Sprintf("ppchg-%d-%06d", time.Now().UnixNano(), seq)

	metaCopy := make(map[string]string, len(meta))
	for k, v := range meta {
		metaCopy[k] = v
	}

	change := &StateChange{
		ID:          id,
		Op:          op,
		Ref:         ref,
		Meta:        metaCopy,
		RequestedAt: time.Now(),
	}

	s.mu.Lock()
	s.requested = append(s.requested, change)
	n := len(s.requested)
	s.mu.Unlock()

	s.log.Debug("portproxy: change %s added to requested (total=%d)", id, n)
	s.log.Info("portproxy: state change requested: id=%s op=%s ref=%s", id, op, ref)
	return change
}

// ConfirmSuccess moves the change identified by changeID from the requested
// bucket into the active bucket.
//
// Returns ErrChangeNotFound if no requested change with that ID exists.
func (s *State) ConfirmSuccess(changeID, resultRef string) (*ActiveRecord, error) {
	s.log.LowTrace("portproxy: confirming success: id=%s resultRef=%s", changeID, resultRef)

	s.mu.Lock()
	defer s.mu.Unlock()

	idx, change := s.findRequested(changeID)
	if change == nil {
		s.log.Error("portproxy: ConfirmSuccess: change %q not found", changeID)
		return nil, fmt.Errorf("%w: %s", ErrChangeNotFound, changeID)
	}

	s.requested = removeAt(s.requested, idx)

	rec := &ActiveRecord{
		Change:      change,
		ResultRef:   resultRef,
		ConfirmedAt: time.Now(),
	}
	s.active = append(s.active, rec)

	s.log.Info("portproxy: state change confirmed active: id=%s op=%s ref=%s", changeID, change.Op, change.Ref)
	return rec, nil
}

// RecordFailure moves the change identified by changeID from the requested
// bucket into the failed bucket.
//
// Returns ErrChangeNotFound if no requested change with that ID exists.
func (s *State) RecordFailure(changeID string, err error) (*FailedRecord, error) {
	s.log.LowTrace("portproxy: recording failure: id=%s err=%v", changeID, err)

	if err == nil {
		err = errors.New("(no error provided)")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	idx, change := s.findRequested(changeID)
	if change == nil {
		s.log.Error("portproxy: RecordFailure: change %q not found", changeID)
		return nil, fmt.Errorf("%w: %s", ErrChangeNotFound, changeID)
	}

	s.requested = removeAt(s.requested, idx)

	rec := &FailedRecord{
		Change:   change,
		Err:      err.Error(),
		FailedAt: time.Now(),
	}
	s.failed = append(s.failed, rec)

	s.log.Warn("portproxy: state change failed: id=%s op=%s ref=%s err=%s",
		changeID, change.Op, change.Ref, err)
	return rec, nil
}

// Abandon moves the change identified by changeID from the requested bucket
// into the abandoned bucket with the supplied reason.
//
// Returns ErrChangeNotFound if no requested change with that ID exists.
func (s *State) Abandon(changeID, reason string) (*AbandonedRecord, error) {
	s.log.LowTrace("portproxy: abandoning change: id=%s reason=%q", changeID, reason)

	if reason == "" {
		reason = "(no reason given)"
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	idx, change := s.findRequested(changeID)
	if change == nil {
		s.log.Error("portproxy: Abandon: change %q not found", changeID)
		return nil, fmt.Errorf("%w: %s", ErrChangeNotFound, changeID)
	}

	s.requested = removeAt(s.requested, idx)

	rec := &AbandonedRecord{
		Change:      change,
		Reason:      reason,
		AbandonedAt: time.Now(),
	}
	s.abandoned = append(s.abandoned, rec)

	s.log.Warn("portproxy: state change abandoned: id=%s op=%s ref=%s reason=%q",
		changeID, change.Op, change.Ref, reason)
	return rec, nil
}

// ── Queries ──────────────────────────────────────────────────────────────────

// Summary returns live counts for all four buckets.
func (s *State) Summary() (requested, active, failed, abandoned int) {
	s.mu.RLock()
	requested = len(s.requested)
	active = len(s.active)
	failed = len(s.failed)
	abandoned = len(s.abandoned)
	s.mu.RUnlock()
	return
}

// ── Status ───────────────────────────────────────────────────────────────────

// Status represents which bucket a change currently lives in.
type Status string

const (
	StatusRequested Status = "requested"
	StatusActive    Status = "active"
	StatusFailed    Status = "failed"
	StatusAbandoned Status = "abandoned"
)

// ── Errors ───────────────────────────────────────────────────────────────────

// ErrChangeNotFound is returned when a changeID is not present in the
// requested bucket.
var ErrChangeNotFound = errors.New("portproxy state change not found")

// ── Internal helpers ─────────────────────────────────────────────────────────

func (s *State) findRequested(changeID string) (int, *StateChange) {
	for i, c := range s.requested {
		if c.ID == changeID {
			return i, c
		}
	}
	return -1, nil
}

func removeAt(sl []*StateChange, i int) []*StateChange {
	last := len(sl) - 1
	sl[i] = sl[last]
	sl[last] = nil
	return sl[:last]
}
