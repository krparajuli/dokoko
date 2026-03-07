// store.go — in-process image inventory with fingerprinting and lifecycle tracking.
//
// The Store tracks every image this process has ever observed, independently of
// Docker itself.  That separation enables three things:
//
//  1. Persistent identity  — images are keyed by their content-addressable
//     DockerID so the store survives retags and name changes.
//
//  2. Band attribution     — every record carries an Origin that answers
//     "did dokoko introduce this image, or did it exist/appear on its own?"
//
//  3. Out-of-band detection — Reconcile compares a fresh Docker list against
//     the store and marks images that vanished without going through dokoko.
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
//	           Register()   ← re-pull restores to present
//	              ▼
//	          [present]

package dockerimagestate

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"dokoko.ai/dokoko/pkg/logger"
)

// ── Errors ────────────────────────────────────────────────────────────────────

// ErrImageNotFound is returned by Mark* operations when the DockerID is absent.
var ErrImageNotFound = errors.New("image not found in store")

// ── Origin ────────────────────────────────────────────────────────────────────

// Origin describes how an image entered the store.
type Origin string

const (
	// OriginInBand means the image was pulled, tagged, or otherwise introduced
	// through dokoko's own operations.
	OriginInBand Origin = "in_band"

	// OriginOutOfBand means the image was already present in Docker when first
	// observed, or appeared without going through dokoko.
	OriginOutOfBand Origin = "out_of_band"
)

// ── ImageStatus ───────────────────────────────────────────────────────────────

// ImageStatus describes the known lifecycle state of a tracked image.
type ImageStatus string

const (
	// ImageStatusPresent means the image is confirmed present in Docker.
	ImageStatusPresent ImageStatus = "present"

	// ImageStatusDeleted means the image was removed through dokoko (in-band).
	ImageStatusDeleted ImageStatus = "deleted"

	// ImageStatusDeletedOutOfBand means the image disappeared from Docker
	// without going through dokoko, detected during Reconcile.
	ImageStatusDeletedOutOfBand ImageStatus = "deleted_out_of_band"

	// ImageStatusErrored means an operation on this image resulted in an error.
	// ErrMsg on the record holds the details.
	ImageStatusErrored ImageStatus = "errored"
)

// ── Fingerprint ───────────────────────────────────────────────────────────────

// Fingerprint uniquely identifies image content independently of tags or names.
// Both fields are derived deterministically from Docker's own metadata so a
// fingerprint can be compared across daemon restarts and tag changes.
type Fingerprint struct {
	// ConfigDigest is the hex-encoded SHA256 of the image configuration JSON
	// (identical to the Docker image ID with the "sha256:" prefix stripped).
	// This is the canonical content identity: two images with the same
	// ConfigDigest are byte-for-byte identical in every layer and config.
	ConfigDigest string

	// LayerChain is the colon-joined list of layer content digests in layer
	// order.  Two images with the same LayerChain share all filesystem layers
	// even if their configs differ (e.g. they carry different labels or env).
	LayerChain string
}

// ── RegisterParams ────────────────────────────────────────────────────────────

// RegisterParams carries all metadata needed to upsert an image into the Store.
// Populate it from dockertypes.ImageInspect at the call site.
type RegisterParams struct {
	DockerID       string    // full "sha256:..." image ID
	RepoTags       []string  // e.g. ["ubuntu:22.04", "ubuntu:latest"]
	RepoDigests    []string  // e.g. ["ubuntu@sha256:..."]
	OS             string    // e.g. "linux"
	Architecture   string    // e.g. "amd64"
	Variant        string    // e.g. "v8" for arm64/v8
	Size           int64     // compressed size in bytes
	ImageCreatedAt time.Time // when Docker built/tagged the image
	Layers         []string  // RootFS.Layers for fingerprint computation
	Origin         Origin    // InBand or OutOfBand
}

// ── ImageRecord ───────────────────────────────────────────────────────────────

// ImageRecord is the store's complete view of one tracked image.
// Fields are set at registration and updated on subsequent Register calls or
// lifecycle transitions.  Callers receive copies; the internal pointer is
// never exposed.
type ImageRecord struct {
	// Identity
	DockerID    string      // full "sha256:..." image ID
	ShortID     string      // first 12 hex characters (for display)
	Fingerprint Fingerprint // content-addressable identity

	// Labels / references
	RepoTags    []string // repo:tag references currently pointing at this image
	RepoDigests []string // repo@sha256:... content-addressed references

	// Platform
	OS           string // e.g. "linux"
	Architecture string // e.g. "amd64", "arm64"
	Variant      string // e.g. "v8"

	// Size
	Size int64 // compressed image size in bytes

	// Timestamps
	ImageCreatedAt time.Time // when Docker reports the image was created
	RegisteredAt   time.Time // first time this store observed the image
	UpdatedAt      time.Time // last mutation of any field

	// Lifecycle
	Origin Origin      // how this image entered the store
	Status ImageStatus // current known state
	ErrMsg string      // non-empty only when Status == ImageStatusErrored
}

// clone returns a shallow-copy-safe snapshot of the record.
func (r *ImageRecord) clone() *ImageRecord {
	cp := *r
	cp.RepoTags = copyStringSlice(r.RepoTags)
	cp.RepoDigests = copyStringSlice(r.RepoDigests)
	return &cp
}

// ── Store ─────────────────────────────────────────────────────────────────────

// Store is an in-memory registry of all images known to this process.
// All exported methods are safe for concurrent use.
type Store struct {
	mu      sync.RWMutex
	records map[string]*ImageRecord // keyed by DockerID
	log     *logger.Logger
}

// NewStore returns an empty, ready-to-use Store.
func NewStore(log *logger.Logger) *Store {
	log.LowTrace("initialising image store")
	s := &Store{
		records: make(map[string]*ImageRecord),
		log:     log,
	}
	log.Info("image store ready")
	return s
}

// ── Mutations ────────────────────────────────────────────────────────────────

// Register upserts an image record.
//
// First call: creates a new record with Status=Present.
// Subsequent calls: updates mutable fields (tags, digests, size) and sets
// Status back to Present if it was previously in a non-present state.
// Origin is upgraded from OutOfBand to InBand if the caller supplies InBand,
// but never downgraded.
func (s *Store) Register(p RegisterParams) *ImageRecord {
	s.log.LowTrace("register: dockerID=%s origin=%s", shortDockerID(p.DockerID), p.Origin)

	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	rec, exists := s.records[p.DockerID]

	if !exists {
		rec = &ImageRecord{
			DockerID:       p.DockerID,
			ShortID:        computeShortID(p.DockerID),
			Fingerprint:    computeFingerprint(p.DockerID, p.Layers),
			RegisteredAt:   now,
			ImageCreatedAt: p.ImageCreatedAt,
			Origin:         p.Origin,
		}
		s.log.Debug("register: new record created: id=%s origin=%s", rec.ShortID, rec.Origin)
	} else {
		// Upgrade origin: OutOfBand → InBand if we now manage it.
		if p.Origin == OriginInBand && rec.Origin == OriginOutOfBand {
			s.log.Debug("register: upgrading origin OutOfBand→InBand for %s", rec.ShortID)
			rec.Origin = OriginInBand
		}
		s.log.Debug("register: updating existing record: id=%s prev_status=%s", rec.ShortID, rec.Status)
	}

	// Always refresh mutable fields.
	rec.RepoTags = copyStringSlice(p.RepoTags)
	rec.RepoDigests = copyStringSlice(p.RepoDigests)
	rec.OS = p.OS
	rec.Architecture = p.Architecture
	rec.Variant = p.Variant
	rec.Size = p.Size
	rec.Status = ImageStatusPresent
	rec.ErrMsg = ""
	rec.UpdatedAt = now

	s.records[p.DockerID] = rec

	s.log.Info("image registered: id=%s tags=%v origin=%s", rec.ShortID, rec.RepoTags, rec.Origin)
	return rec.clone()
}

// MarkDeleted transitions the image to ImageStatusDeleted (in-band removal).
// Returns ErrImageNotFound if the DockerID is not in the store.
func (s *Store) MarkDeleted(dockerID string) error {
	return s.transition(dockerID, ImageStatusDeleted, "")
}

// MarkDeletedOutOfBand transitions the image to ImageStatusDeletedOutOfBand.
// Call this when Reconcile detects the image vanished from Docker without going
// through dokoko.  Returns ErrImageNotFound if the DockerID is unknown.
func (s *Store) MarkDeletedOutOfBand(dockerID string) error {
	return s.transition(dockerID, ImageStatusDeletedOutOfBand, "")
}

// MarkErrored records an error against the image.
// Returns ErrImageNotFound if the DockerID is unknown.
func (s *Store) MarkErrored(dockerID, errMsg string) error {
	if errMsg == "" {
		errMsg = "(no error message provided)"
	}
	return s.transition(dockerID, ImageStatusErrored, errMsg)
}

// MarkPresent transitions a previously-deleted or errored image back to present.
// Useful when the actor confirms an image is available again (e.g. after a pull
// that reuses an existing ID).  Returns ErrImageNotFound if the DockerID is unknown.
func (s *Store) MarkPresent(dockerID string) error {
	return s.transition(dockerID, ImageStatusPresent, "")
}

// transition is the internal helper that updates Status + ErrMsg atomically.
func (s *Store) transition(dockerID string, status ImageStatus, errMsg string) error {
	s.log.LowTrace("transition: id=%s → %s", shortDockerID(dockerID), status)

	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.records[dockerID]
	if !ok {
		s.log.Warn("transition: dockerID %q not found", shortDockerID(dockerID))
		return fmt.Errorf("%w: %s", ErrImageNotFound, dockerID)
	}

	prev := rec.Status
	rec.Status = status
	rec.ErrMsg = errMsg
	rec.UpdatedAt = time.Now()

	s.log.Debug("transition: %s %s → %s", rec.ShortID, prev, status)
	if errMsg != "" {
		s.log.Warn("image errored: id=%s err=%s", rec.ShortID, errMsg)
	} else {
		s.log.Info("image status changed: id=%s %s → %s", rec.ShortID, prev, status)
	}
	return nil
}

// ── Queries ───────────────────────────────────────────────────────────────────

// Get returns a snapshot copy of the record for dockerID.
// Returns false if the image is not in the store.
func (s *Store) Get(dockerID string) (*ImageRecord, bool) {
	s.log.Trace("Get: %s", shortDockerID(dockerID))
	s.mu.RLock()
	rec, ok := s.records[dockerID]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return rec.clone(), true
}

// All returns snapshot copies of every record in the store.
func (s *Store) All() []*ImageRecord {
	s.log.Trace("All: reading store")
	s.mu.RLock()
	out := make([]*ImageRecord, 0, len(s.records))
	for _, r := range s.records {
		out = append(out, r.clone())
	}
	s.mu.RUnlock()
	s.log.Debug("All: returned %d records", len(out))
	return out
}

// ByStatus returns snapshot copies of all records whose Status matches.
func (s *Store) ByStatus(status ImageStatus) []*ImageRecord {
	s.log.Trace("ByStatus: %s", status)
	s.mu.RLock()
	var out []*ImageRecord
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
func (s *Store) Size() int {
	s.mu.RLock()
	n := len(s.records)
	s.mu.RUnlock()
	return n
}

// ── Reconcile ─────────────────────────────────────────────────────────────────

// Reconcile compares a snapshot of currently-live Docker image IDs against the
// store and marks any Present image that is absent from liveIDs as
// ImageStatusDeletedOutOfBand.
//
// It returns the DockerIDs of every record that was transitioned.  Call this
// after a fresh ops.List to keep the store consistent with Docker's reality.
func (s *Store) Reconcile(liveIDs []string) []string {
	s.log.LowTrace("Reconcile: %d live IDs provided", len(liveIDs))

	live := make(map[string]struct{}, len(liveIDs))
	for _, id := range liveIDs {
		live[id] = struct{}{}
	}

	s.mu.Lock()
	var affected []string
	for id, rec := range s.records {
		if rec.Status != ImageStatusPresent {
			continue
		}
		if _, found := live[id]; !found {
			prev := rec.Status
			rec.Status = ImageStatusDeletedOutOfBand
			rec.UpdatedAt = time.Now()
			affected = append(affected, id)
			s.log.Warn("reconcile: out-of-band deletion detected: id=%s tags=%v prev=%s",
				rec.ShortID, rec.RepoTags, prev)
		}
	}
	s.mu.Unlock()

	s.log.Info("reconcile complete: %d/%d images marked deleted_out_of_band",
		len(affected), len(s.records))
	return affected
}

// ── Internal helpers ──────────────────────────────────────────────────────────

func computeShortID(dockerID string) string {
	id := strings.TrimPrefix(dockerID, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func computeFingerprint(dockerID string, layers []string) Fingerprint {
	return Fingerprint{
		ConfigDigest: strings.TrimPrefix(dockerID, "sha256:"),
		LayerChain:   strings.Join(layers, ":"),
	}
}

func shortDockerID(id string) string {
	s := strings.TrimPrefix(id, "sha256:")
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func copyStringSlice(src []string) []string {
	if len(src) == 0 {
		return nil
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}
