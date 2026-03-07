// store.go — in-process build history store.
//
// The BuildStore records each build attempt initiated through dokoko, whether
// it succeeded, failed, or was abandoned.  Unlike the image store (which
// tracks the resulting artifact), the build store tracks the build event
// itself — the intent, the inputs, and the outcome.
//
// Builds are keyed by the state-change ID so the actor can correlate records
// with the state machine without an additional layer of ID mapping.
//
// Lifecycle diagram (BuildStatus):
//
//	RegisterBuild()
//	  ──► [pending]
//	           │
//	    ┌──────┼──────────┐
//	    ▼      ▼          ▼
//	[succeeded] [failed] [abandoned]
//
// Builds do not have an out-of-band deletion concept: a build event is an
// immutable record once it settles.  There is no Reconcile.

package dockerbuildstate

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"dokoko.ai/dokoko/pkg/logger"
)

// ── Errors ────────────────────────────────────────────────────────────────────

// ErrBuildNotFound is returned when a changeID is not in the build store.
var ErrBuildNotFound = errors.New("build record not found in store")

// ── BuildStatus ───────────────────────────────────────────────────────────────

// BuildStatus describes the current lifecycle state of a build record.
type BuildStatus string

const (
	// BuildStatusPending means the build was submitted but has not yet completed.
	BuildStatusPending BuildStatus = "pending"

	// BuildStatusSucceeded means the build completed and an image was produced.
	BuildStatusSucceeded BuildStatus = "succeeded"

	// BuildStatusFailed means the build completed with an error (no image produced).
	BuildStatusFailed BuildStatus = "failed"

	// BuildStatusAbandoned means the build was never executed (context cancelled
	// or actor shut down before the worker picked it up).
	BuildStatusAbandoned BuildStatus = "abandoned"
)

// ── RegisterBuildParams ───────────────────────────────────────────────────────

// RegisterBuildParams carries the build inputs needed to create a pending
// build record.  Populate it when the actor submits the build to the state
// machine.
type RegisterBuildParams struct {
	ChangeID   string   // state machine change ID (used as the store key)
	Tags       []string // requested image tags, e.g. ["myapp:latest", "myapp:v1.2"]
	Dockerfile string   // path to the Dockerfile (empty = "Dockerfile" in context)
	ContextDir string   // build context directory or URL
	Platform   string   // target platform, e.g. "linux/amd64" (empty = daemon default)
}

// ── BuildRecord ───────────────────────────────────────────────────────────────

// BuildRecord is the store's view of one build attempt.
// Callers receive copies; the internal pointer is never exposed.
type BuildRecord struct {
	// Identity
	ChangeID string // state-machine change ID (primary key)

	// Build inputs (immutable after registration)
	Tags       []string // requested image tags
	Dockerfile string   // Dockerfile path
	ContextDir string   // build context
	Platform   string   // target platform

	// Outcome (set when build settles)
	ResultImageID string // sha256 of produced image (empty until succeeded)
	ErrMsg        string // non-empty when failed or abandoned

	// Timestamps
	RegisteredAt time.Time // when RegisterBuild was called
	FinishedAt   time.Time // zero until the build settles

	// Lifecycle
	Status BuildStatus // current state
}

// clone returns a safe snapshot of the record.
func (r *BuildRecord) clone() *BuildRecord {
	cp := *r
	cp.Tags = copyStringSliceBuild(r.Tags)
	return &cp
}

// ── BuildStore ────────────────────────────────────────────────────────────────

// BuildStore is an in-memory log of all build attempts known to this process.
// All exported methods are safe for concurrent use.
type BuildStore struct {
	mu      sync.RWMutex
	records map[string]*BuildRecord // keyed by ChangeID
	log     *logger.Logger
}

// NewBuildStore returns an empty, ready-to-use BuildStore.
func NewBuildStore(log *logger.Logger) *BuildStore {
	log.LowTrace("initialising build store")
	s := &BuildStore{
		records: make(map[string]*BuildRecord),
		log:     log,
	}
	log.Info("build store ready")
	return s
}

// ── Mutations ─────────────────────────────────────────────────────────────────

// RegisterBuild creates a new build record with Status=Pending.
// If a record with the same ChangeID already exists it is returned as-is
// (idempotent guard against double-registration).
func (s *BuildStore) RegisterBuild(p RegisterBuildParams) *BuildRecord {
	s.log.LowTrace("RegisterBuild: changeID=%s tags=%v", p.ChangeID, p.Tags)

	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	if rec, exists := s.records[p.ChangeID]; exists {
		s.log.Debug("RegisterBuild: duplicate registration for %s, returning existing", p.ChangeID)
		return rec.clone()
	}

	rec := &BuildRecord{
		ChangeID:     p.ChangeID,
		Tags:         copyStringSliceBuild(p.Tags),
		Dockerfile:   p.Dockerfile,
		ContextDir:   p.ContextDir,
		Platform:     p.Platform,
		Status:       BuildStatusPending,
		RegisteredAt: now,
	}
	s.records[p.ChangeID] = rec

	s.log.Info("build registered: changeID=%s tags=%v platform=%q",
		p.ChangeID, p.Tags, p.Platform)
	return rec.clone()
}

// MarkSucceeded transitions a pending build to succeeded and records the
// resulting image ID.
// Returns ErrBuildNotFound if the changeID is unknown.
func (s *BuildStore) MarkSucceeded(changeID, imageID string) error {
	s.log.LowTrace("MarkSucceeded: changeID=%s imageID=%s", changeID, shortBuildID(imageID))

	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.records[changeID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrBuildNotFound, changeID)
	}

	prev := rec.Status
	rec.Status = BuildStatusSucceeded
	rec.ResultImageID = imageID
	rec.FinishedAt = time.Now()

	s.log.Debug("build settled: %s %s → succeeded (imageID=%s)", changeID, prev, shortBuildID(imageID))
	s.log.Info("build succeeded: changeID=%s tags=%v imageID=%s",
		changeID, rec.Tags, shortBuildID(imageID))
	return nil
}

// MarkFailed transitions a pending build to failed and records the error.
// Returns ErrBuildNotFound if the changeID is unknown.
func (s *BuildStore) MarkFailed(changeID, errMsg string) error {
	if errMsg == "" {
		errMsg = "(no error message provided)"
	}
	s.log.LowTrace("MarkFailed: changeID=%s", changeID)

	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.records[changeID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrBuildNotFound, changeID)
	}

	prev := rec.Status
	rec.Status = BuildStatusFailed
	rec.ErrMsg = errMsg
	rec.FinishedAt = time.Now()

	s.log.Warn("build failed: changeID=%s tags=%v prev=%s err=%s",
		changeID, rec.Tags, prev, errMsg)
	return nil
}

// MarkAbandoned transitions a pending build to abandoned with a reason.
// Returns ErrBuildNotFound if the changeID is unknown.
func (s *BuildStore) MarkAbandoned(changeID, reason string) error {
	if reason == "" {
		reason = "(no reason provided)"
	}
	s.log.LowTrace("MarkAbandoned: changeID=%s reason=%q", changeID, reason)

	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.records[changeID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrBuildNotFound, changeID)
	}

	prev := rec.Status
	rec.Status = BuildStatusAbandoned
	rec.ErrMsg = reason
	rec.FinishedAt = time.Now()

	s.log.Warn("build abandoned: changeID=%s tags=%v prev=%s reason=%q",
		changeID, rec.Tags, prev, reason)
	return nil
}

// ── Queries ───────────────────────────────────────────────────────────────────

// Get returns a snapshot copy of the build record for changeID.
// Returns false if the changeID is not in the store.
func (s *BuildStore) Get(changeID string) (*BuildRecord, bool) {
	s.log.Trace("Get: %s", changeID)
	s.mu.RLock()
	rec, ok := s.records[changeID]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return rec.clone(), true
}

// All returns snapshot copies of every build record in the store.
func (s *BuildStore) All() []*BuildRecord {
	s.log.Trace("All: reading build store")
	s.mu.RLock()
	out := make([]*BuildRecord, 0, len(s.records))
	for _, r := range s.records {
		out = append(out, r.clone())
	}
	s.mu.RUnlock()
	s.log.Debug("All: returned %d records", len(out))
	return out
}

// ByStatus returns snapshot copies of all build records with the given status.
func (s *BuildStore) ByStatus(status BuildStatus) []*BuildRecord {
	s.log.Trace("ByStatus: %s", status)
	s.mu.RLock()
	var out []*BuildRecord
	for _, r := range s.records {
		if r.Status == status {
			out = append(out, r.clone())
		}
	}
	s.mu.RUnlock()
	s.log.Debug("ByStatus %s: %d records", status, len(out))
	return out
}

// Size returns the number of build records in the store.
func (s *BuildStore) Size() int {
	s.mu.RLock()
	n := len(s.records)
	s.mu.RUnlock()
	return n
}

// ── Internal helpers ──────────────────────────────────────────────────────────

func copyStringSliceBuild(src []string) []string {
	if len(src) == 0 {
		return nil
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}

func shortBuildID(id string) string {
	s := strings.TrimPrefix(id, "sha256:")
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
