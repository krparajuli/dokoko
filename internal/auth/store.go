// Package auth provides user authentication and session management.
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// Role represents a user's permission level.
type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
)

// ErrBadCredentials is returned when username or password does not match.
var ErrBadCredentials = errors.New("invalid username or password")

// User holds static user configuration.
type User struct {
	Username string
	Password string // plain text — acceptable for a local developer tool
	Role     Role
}

// Session represents an authenticated browser session.
type Session struct {
	Token     string
	Username  string
	Role      Role
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Store is an in-memory user and session store.
type Store struct {
	mu       sync.RWMutex
	users    map[string]*User    // username → User
	sessions map[string]*Session // token → Session
}

// NewStore creates a Store pre-populated with the given users.
func NewStore(users []User) *Store {
	s := &Store{
		users:    make(map[string]*User, len(users)),
		sessions: make(map[string]*Session),
	}
	for i := range users {
		u := users[i]
		s.users[u.Username] = &u
	}
	return s
}

// Authenticate checks username/password and returns the User on success.
func (s *Store) Authenticate(username, password string) (*User, error) {
	s.mu.RLock()
	u, ok := s.users[username]
	s.mu.RUnlock()
	if !ok || u.Password != password {
		return nil, ErrBadCredentials
	}
	return u, nil
}

// CreateSession generates a new session token and stores the session.
func (s *Store) CreateSession(username string, role Role, ttl time.Duration) *Session {
	token := generateToken()
	now := time.Now()
	sess := &Session{
		Token:     token,
		Username:  username,
		Role:      role,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
	s.mu.Lock()
	s.sessions[token] = sess
	s.mu.Unlock()
	return sess
}

// GetSession looks up a session by token; returns false if missing or expired.
func (s *Store) GetSession(token string) (*Session, bool) {
	s.mu.RLock()
	sess, ok := s.sessions[token]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if time.Now().After(sess.ExpiresAt) {
		s.DeleteSession(token)
		return nil, false
	}
	return sess, true
}

// DeleteSession removes a session from the store.
func (s *Store) DeleteSession(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

// PruneExpired removes all sessions whose ExpiresAt is in the past.
func (s *Store) PruneExpired() {
	now := time.Now()
	s.mu.Lock()
	for token, sess := range s.sessions {
		if now.After(sess.ExpiresAt) {
			delete(s.sessions, token)
		}
	}
	s.mu.Unlock()
}

// generateToken returns a cryptographically random 64-character hex string.
func generateToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("auth: failed to generate random token: " + err.Error())
	}
	return hex.EncodeToString(b)
}
