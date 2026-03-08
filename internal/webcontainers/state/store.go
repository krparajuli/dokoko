package webcontainersstate

import (
	"sync"
	"time"

	"dokoko.ai/dokoko/pkg/logger"
)

// ── SessionStatus ─────────────────────────────────────────────────────────────

// SessionStatus tracks the lifecycle of a user's container session.
type SessionStatus string

const (
	StatusProvisioning SessionStatus = "provisioning"
	StatusReady        SessionStatus = "ready"
	StatusTerminating  SessionStatus = "terminating"
	StatusStopped      SessionStatus = "stopped"
	StatusError        SessionStatus = "error"
)

// ── UserSession ───────────────────────────────────────────────────────────────

// UserSession records the mapping between a user ID and their web container.
// All fields are set by the actor after the ops layer completes.
type UserSession struct {
	UserID        string        // opaque identifier from the client
	CatalogID     string        // e.g. "ubuntu"
	ContainerName string        // "wc-<safeUserID>"
	ContainerID   string        // Docker container ID
	HostPort      uint16        // host-side port for the ttyd WebSocket
	Status        SessionStatus
	ErrorMsg      string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ── Store ─────────────────────────────────────────────────────────────────────

// Store is the in-memory registry of UserSession records.
// All methods are safe for concurrent use.
type Store struct {
	mu       sync.RWMutex
	sessions map[string]*UserSession // key: UserID
	log      *logger.Logger
}

// NewStore returns an empty, ready-to-use Store.
func NewStore(log *logger.Logger) *Store {
	log.LowTrace("initialising webcontainer session store")
	s := &Store{
		sessions: make(map[string]*UserSession),
		log:      log,
	}
	log.Info("webcontainer session store ready")
	return s
}

// SetSession creates or replaces the session for sess.UserID.
// A copy is stored so callers may reuse the pointer safely.
func (s *Store) SetSession(sess *UserSession) {
	s.log.Debug("webcontainer store: set session userID=%s status=%s", sess.UserID, sess.Status)
	cp := *sess
	cp.UpdatedAt = time.Now()
	s.mu.Lock()
	s.sessions[sess.UserID] = &cp
	s.mu.Unlock()
}

// GetSession returns the session for userID, or nil if absent.
func (s *Store) GetSession(userID string) *UserSession {
	s.mu.RLock()
	sess := s.sessions[userID]
	s.mu.RUnlock()
	if sess == nil {
		return nil
	}
	// Return a copy.
	cp := *sess
	return &cp
}

// DeleteSession removes the session for userID.
func (s *Store) DeleteSession(userID string) {
	s.log.Debug("webcontainer store: delete session userID=%s", userID)
	s.mu.Lock()
	delete(s.sessions, userID)
	s.mu.Unlock()
}

// AllSessions returns a snapshot of all active sessions.
func (s *Store) AllSessions() []*UserSession {
	s.mu.RLock()
	out := make([]*UserSession, 0, len(s.sessions))
	for _, sess := range s.sessions {
		cp := *sess
		out = append(out, &cp)
	}
	s.mu.RUnlock()
	return out
}
