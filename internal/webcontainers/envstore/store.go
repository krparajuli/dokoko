// Package envstore holds per-user environment variables that are injected into
// web-container sessions at provision time and applied live via docker exec.
//
// Variables survive container recreation — they are cleared only when the user
// explicitly removes them via the API.
package envstore

import "sync"

// Store is a thread-safe per-user environment variable registry.
type Store struct {
	mu   sync.RWMutex
	data map[string]map[string]string // userID → key → value
}

// New returns an initialised Store.
func New() *Store {
	return &Store{data: make(map[string]map[string]string)}
}

// Get returns a shallow copy of the env vars for userID.
// Returns nil when the user has no stored vars.
func (s *Store) Get(userID string) map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src := s.data[userID]
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// Replace atomically replaces all env vars for userID with vars.
// Passing a nil or empty map clears all stored vars.
func (s *Store) Replace(userID string, vars map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(vars) == 0 {
		delete(s.data, userID)
		return
	}
	cp := make(map[string]string, len(vars))
	for k, v := range vars {
		cp[k] = v
	}
	s.data[userID] = cp
}

// DeleteAll removes all env vars for userID.
func (s *Store) DeleteAll(userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, userID)
}
