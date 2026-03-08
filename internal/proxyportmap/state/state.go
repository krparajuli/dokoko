// Package proxyportmapstate manages in-memory lifecycle of port-map operations.
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
package proxyportmapstate

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"dokoko.ai/dokoko/pkg/logger"
)

// Operation is the kind of port-map action being requested.
type Operation string

const (
	OpScanAndMap Operation = "scan_and_map"
	OpUnmap      Operation = "unmap"
)

// StateChange is created by RequestChange and is immutable once created.
type StateChange struct {
	ID          string
	Op          Operation
	Ref         string            // user ID
	Meta        map[string]string // optional metadata
	RequestedAt time.Time
}

// ActiveRecord is written by ConfirmSuccess.
type ActiveRecord struct {
	Change      *StateChange
	ResultRef   string
	ConfirmedAt time.Time
}

// FailedRecord is written by RecordFailure.
type FailedRecord struct {
	Change   *StateChange
	Err      string
	FailedAt time.Time
}

// AbandonedRecord is written by Abandon.
type AbandonedRecord struct {
	Change      *StateChange
	Reason      string
	AbandonedAt time.Time
}

// State is the single source of truth for port-map state changes.
// All exported methods are safe for concurrent use.
type State struct {
	mu        sync.RWMutex
	requested []*StateChange
	active    []*ActiveRecord
	failed    []*FailedRecord
	abandoned []*AbandonedRecord

	seq uint64
	log *logger.Logger
}

// New returns an empty, ready-to-use State.
func New(log *logger.Logger) *State {
	log.LowTrace("initialising proxyportmap state")
	s := &State{log: log}
	log.Info("proxyportmap state ready")
	return s
}

// RequestChange registers a new desired state change in the requested bucket.
func (s *State) RequestChange(op Operation, userID string, meta map[string]string) *StateChange {
	s.log.LowTrace("proxyportmap: requesting change op=%s ref=%s", op, userID)

	seq := atomic.AddUint64(&s.seq, 1)
	id := fmt.Sprintf("ppmchg-%d-%06d", time.Now().UnixNano(), seq)

	metaCopy := make(map[string]string, len(meta))
	for k, v := range meta {
		metaCopy[k] = v
	}

	change := &StateChange{
		ID:          id,
		Op:          op,
		Ref:         userID,
		Meta:        metaCopy,
		RequestedAt: time.Now(),
	}

	s.mu.Lock()
	s.requested = append(s.requested, change)
	n := len(s.requested)
	s.mu.Unlock()

	s.log.Info("proxyportmap: change requested id=%s op=%s ref=%s (total=%d)", id, op, userID, n)
	return change
}

// ConfirmSuccess moves changeID from requested → active.
func (s *State) ConfirmSuccess(changeID, resultRef string) (*ActiveRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx, change := s.findRequested(changeID)
	if change == nil {
		return nil, fmt.Errorf("%w: %s", ErrChangeNotFound, changeID)
	}

	s.requested = removeAt(s.requested, idx)
	rec := &ActiveRecord{Change: change, ResultRef: resultRef, ConfirmedAt: time.Now()}
	s.active = append(s.active, rec)

	s.log.Info("proxyportmap: change confirmed id=%s op=%s ref=%s", changeID, change.Op, change.Ref)
	return rec, nil
}

// RecordFailure moves changeID from requested → failed.
func (s *State) RecordFailure(changeID string, err error) (*FailedRecord, error) {
	if err == nil {
		err = errors.New("(no error provided)")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	idx, change := s.findRequested(changeID)
	if change == nil {
		return nil, fmt.Errorf("%w: %s", ErrChangeNotFound, changeID)
	}

	s.requested = removeAt(s.requested, idx)
	rec := &FailedRecord{Change: change, Err: err.Error(), FailedAt: time.Now()}
	s.failed = append(s.failed, rec)

	s.log.Warn("proxyportmap: change failed id=%s op=%s ref=%s err=%v", changeID, change.Op, change.Ref, err)
	return rec, nil
}

// Abandon moves changeID from requested → abandoned.
func (s *State) Abandon(changeID, reason string) (*AbandonedRecord, error) {
	if reason == "" {
		reason = "(no reason given)"
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	idx, change := s.findRequested(changeID)
	if change == nil {
		return nil, fmt.Errorf("%w: %s", ErrChangeNotFound, changeID)
	}

	s.requested = removeAt(s.requested, idx)
	rec := &AbandonedRecord{Change: change, Reason: reason, AbandonedAt: time.Now()}
	s.abandoned = append(s.abandoned, rec)

	s.log.Warn("proxyportmap: change abandoned id=%s reason=%q", changeID, reason)
	return rec, nil
}

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

// ── Errors ────────────────────────────────────────────────────────────────────

var ErrChangeNotFound = errors.New("proxyportmap state change not found")

// ── Internal helpers ──────────────────────────────────────────────────────────

func (s *State) findRequested(id string) (int, *StateChange) {
	for i, c := range s.requested {
		if c.ID == id {
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
