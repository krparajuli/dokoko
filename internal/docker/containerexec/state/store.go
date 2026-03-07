// store.go — in-process exec session store.
//
// The ExecStore tracks exec instances that were successfully created via
// dokoko.  Each record is keyed by the Docker-assigned exec ID.
//
// Lifecycle diagram (ExecStatus):
//
//	RegisterExec()
//	  ──► [created]
//	           │
//	    ┌──────┴──────┐
//	    ▼             ▼
//	[running]     [errored]
//	    │
//	    ▼
//	[finished]
//
// Exec sessions are ephemeral events, not persistent inventory.  There is no
// out-of-band concept or Reconcile method.

package dockercontainerexecstate

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"dokoko.ai/dokoko/pkg/logger"
)

// ── Errors ────────────────────────────────────────────────────────────────────

// ErrExecNotFound is returned when an ExecID is absent from the store.
var ErrExecNotFound = errors.New("exec session not found in store")

// ── ExecStatus ────────────────────────────────────────────────────────────────

// ExecStatus describes the lifecycle state of a tracked exec session.
type ExecStatus string

const (
	// ExecStatusCreated means the exec instance was created but not yet started.
	ExecStatusCreated ExecStatus = "created"

	// ExecStatusRunning means the exec instance is actively executing.
	ExecStatusRunning ExecStatus = "running"

	// ExecStatusFinished means the exec completed (check ExitCode for result).
	ExecStatusFinished ExecStatus = "finished"

	// ExecStatusErrored means an operation on the exec resulted in an error.
	ExecStatusErrored ExecStatus = "errored"
)

// ── RegisterExecParams ────────────────────────────────────────────────────────

// RegisterExecParams carries metadata for a successfully-created exec instance.
// Populate it after a successful exec-create response from the Docker daemon.
type RegisterExecParams struct {
	ExecID      string   // Docker-assigned exec ID
	ContainerID string   // ID of the container this exec runs in
	Cmd         []string // command being executed, e.g. ["/bin/sh", "-c", "ls"]
	Tty         bool     // whether a TTY was allocated
	Detach      bool     // whether the exec runs detached
}

// ── ExecRecord ────────────────────────────────────────────────────────────────

// ExecRecord is the store's view of one exec session.
// Callers receive copies; the internal pointer is never exposed.
type ExecRecord struct {
	// Identity
	ExecID      string // Docker-assigned exec ID
	ContainerID string // container the exec runs in

	// Exec configuration (immutable after creation)
	Cmd    []string // command
	Tty    bool     // TTY allocated
	Detach bool     // detached mode

	// Outcome (set when exec settles)
	ExitCode int    // exit code (valid only when Status == ExecStatusFinished)
	ErrMsg   string // non-empty when Status == ExecStatusErrored

	// Timestamps
	RegisteredAt time.Time // when RegisterExec was called
	StartedAt    time.Time // zero until MarkRunning
	FinishedAt   time.Time // zero until MarkFinished or MarkErrored

	// Lifecycle
	Status ExecStatus // current state
}

// clone returns a safe snapshot of the record.
func (r *ExecRecord) clone() *ExecRecord {
	cp := *r
	cp.Cmd = copyStringSliceExec(r.Cmd)
	return &cp
}

// ── ExecStore ─────────────────────────────────────────────────────────────────

// ExecStore is an in-memory log of all exec sessions known to this process.
// All exported methods are safe for concurrent use.
type ExecStore struct {
	mu      sync.RWMutex
	records map[string]*ExecRecord // keyed by ExecID
	log     *logger.Logger
}

// NewExecStore returns an empty, ready-to-use ExecStore.
func NewExecStore(log *logger.Logger) *ExecStore {
	log.LowTrace("initialising exec store")
	s := &ExecStore{
		records: make(map[string]*ExecRecord),
		log:     log,
	}
	log.Info("exec store ready")
	return s
}

// ── Mutations ─────────────────────────────────────────────────────────────────

// RegisterExec creates a new exec record with Status=Created.
// If a record with the same ExecID already exists it is returned as-is.
func (s *ExecStore) RegisterExec(p RegisterExecParams) *ExecRecord {
	s.log.LowTrace("RegisterExec: execID=%s containerID=%s",
		shortExecID(p.ExecID), shortExecID(p.ContainerID))

	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	if rec, exists := s.records[p.ExecID]; exists {
		s.log.Debug("RegisterExec: duplicate registration for %s, returning existing", shortExecID(p.ExecID))
		return rec.clone()
	}

	rec := &ExecRecord{
		ExecID:       p.ExecID,
		ContainerID:  p.ContainerID,
		Cmd:          copyStringSliceExec(p.Cmd),
		Tty:          p.Tty,
		Detach:       p.Detach,
		Status:       ExecStatusCreated,
		RegisteredAt: now,
	}
	s.records[p.ExecID] = rec

	s.log.Info("exec registered: execID=%s containerID=%s cmd=%v tty=%v",
		shortExecID(p.ExecID), shortExecID(p.ContainerID), p.Cmd, p.Tty)
	return rec.clone()
}

// MarkRunning transitions an exec from created to running.
// Returns ErrExecNotFound if the ExecID is not in the store.
func (s *ExecStore) MarkRunning(execID string) error {
	s.log.LowTrace("MarkRunning: execID=%s", shortExecID(execID))

	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.records[execID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrExecNotFound, execID)
	}

	prev := rec.Status
	rec.Status = ExecStatusRunning
	rec.StartedAt = time.Now()

	s.log.Debug("exec running: %s %s → running", shortExecID(execID), prev)
	return nil
}

// MarkFinished transitions the exec to finished and records the exit code.
// Returns ErrExecNotFound if the ExecID is not in the store.
func (s *ExecStore) MarkFinished(execID string, exitCode int) error {
	s.log.LowTrace("MarkFinished: execID=%s exitCode=%d", shortExecID(execID), exitCode)

	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.records[execID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrExecNotFound, execID)
	}

	prev := rec.Status
	rec.Status = ExecStatusFinished
	rec.ExitCode = exitCode
	rec.FinishedAt = time.Now()

	s.log.Info("exec finished: %s %s → finished (exit=%d)", shortExecID(execID), prev, exitCode)
	return nil
}

// MarkErrored transitions the exec to errored and records the error message.
// Returns ErrExecNotFound if the ExecID is not in the store.
func (s *ExecStore) MarkErrored(execID, errMsg string) error {
	if errMsg == "" {
		errMsg = "(no error message provided)"
	}
	s.log.LowTrace("MarkErrored: execID=%s", shortExecID(execID))

	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.records[execID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrExecNotFound, execID)
	}

	prev := rec.Status
	rec.Status = ExecStatusErrored
	rec.ErrMsg = errMsg
	rec.FinishedAt = time.Now()

	s.log.Warn("exec errored: %s %s → errored err=%q", shortExecID(execID), prev, errMsg)
	return nil
}

// ── Queries ───────────────────────────────────────────────────────────────────

// Get returns a snapshot copy of the record for execID.
// Returns false if the ExecID is not in the store.
func (s *ExecStore) Get(execID string) (*ExecRecord, bool) {
	s.log.Trace("Get: %s", shortExecID(execID))
	s.mu.RLock()
	rec, ok := s.records[execID]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return rec.clone(), true
}

// All returns snapshot copies of every exec record in the store.
func (s *ExecStore) All() []*ExecRecord {
	s.log.Trace("All: reading exec store")
	s.mu.RLock()
	out := make([]*ExecRecord, 0, len(s.records))
	for _, r := range s.records {
		out = append(out, r.clone())
	}
	s.mu.RUnlock()
	s.log.Debug("All: returned %d records", len(out))
	return out
}

// ByStatus returns snapshot copies of all records with the given status.
func (s *ExecStore) ByStatus(status ExecStatus) []*ExecRecord {
	s.log.Trace("ByStatus: %s", status)
	s.mu.RLock()
	var out []*ExecRecord
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
func (s *ExecStore) Size() int {
	s.mu.RLock()
	n := len(s.records)
	s.mu.RUnlock()
	return n
}

// ── Internal helpers ──────────────────────────────────────────────────────────

func copyStringSliceExec(src []string) []string {
	if len(src) == 0 {
		return nil
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}

func shortExecID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
