// Package auth provides user authentication and session management.
package auth

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Role represents a user's permission level.
type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
)

// Sentinel errors.
var (
	ErrBadCredentials = errors.New("invalid username or password")
	ErrUserExists     = errors.New("username already exists")
	ErrNotFound       = errors.New("user not found")
	ErrLastAdmin      = errors.New("cannot delete the last admin user")
)

// User holds user configuration.
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

// Store persists users and sessions in SQLite.
type Store struct {
	db *sql.DB
}

// New creates a Store backed by the given database.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// SeedUser inserts a user if no user with that username exists yet.
// Used only for admin seeding at startup; not for general user creation.
func (s *Store) SeedUser(u User) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO users(username, password, role, created_at) VALUES (?, ?, ?, ?)`,
		u.Username, u.Password, string(u.Role), time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("auth: seed user: %w", err)
	}
	return nil
}

// Authenticate checks username/password and returns the User on success.
func (s *Store) Authenticate(username, password string) (*User, error) {
	var u User
	var role string
	err := s.db.QueryRow(
		`SELECT username, password, role FROM users WHERE username = ?`, username,
	).Scan(&u.Username, &u.Password, &role)
	if err == sql.ErrNoRows || (err == nil && u.Password != password) {
		return nil, ErrBadCredentials
	}
	if err != nil {
		return nil, fmt.Errorf("auth: authenticate: %w", err)
	}
	u.Role = Role(role)
	return &u, nil
}

// CreateSession generates a new session token and stores the session.
func (s *Store) CreateSession(username string, role Role, ttl time.Duration) *Session {
	token := generateToken()
	now := time.Now().UTC()
	exp := now.Add(ttl)
	_, err := s.db.Exec(
		`INSERT INTO sessions(token, username, role, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
		token, username, string(role),
		now.Format(time.RFC3339), exp.Format(time.RFC3339),
	)
	if err != nil {
		// Session creation failure is non-recoverable in normal operation.
		panic("auth: create session: " + err.Error())
	}
	return &Session{
		Token:     token,
		Username:  username,
		Role:      role,
		CreatedAt: now,
		ExpiresAt: exp,
	}
}

// GetSession looks up a session by token; returns false if missing or expired.
func (s *Store) GetSession(token string) (*Session, bool) {
	var sess Session
	var roleStr, createdStr, expiresStr string
	err := s.db.QueryRow(
		`SELECT token, username, role, created_at, expires_at FROM sessions
		 WHERE token = ? AND expires_at > datetime('now')`,
		token,
	).Scan(&sess.Token, &sess.Username, &roleStr, &createdStr, &expiresStr)
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		return nil, false
	}
	sess.Role = Role(roleStr)
	if t, err := time.Parse(time.RFC3339, createdStr); err == nil {
		sess.CreatedAt = t
	}
	if t, err := time.Parse(time.RFC3339, expiresStr); err == nil {
		sess.ExpiresAt = t
	}
	return &sess, true
}

// DeleteSession removes a session from the store.
func (s *Store) DeleteSession(token string) {
	s.db.Exec(`DELETE FROM sessions WHERE token = ?`, token) //nolint:errcheck
}

// ListUsers returns all users with passwords omitted.
func (s *Store) ListUsers() []User {
	rows, err := s.db.Query(`SELECT username, role FROM users ORDER BY username`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var role string
		if err := rows.Scan(&u.Username, &role); err == nil {
			u.Role = Role(role)
			out = append(out, u)
		}
	}
	return out
}

// CreateUser adds a new user. Returns ErrUserExists if the username is taken.
func (s *Store) CreateUser(u User) error {
	_, err := s.db.Exec(
		`INSERT INTO users(username, password, role, created_at) VALUES (?, ?, ?, ?)`,
		u.Username, u.Password, string(u.Role), time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		// SQLite UNIQUE constraint violation — driver returns an error containing "UNIQUE constraint failed"
		if isUniqueViolation(err) {
			return ErrUserExists
		}
		return fmt.Errorf("auth: create user: %w", err)
	}
	return nil
}

// DeleteUser removes a user (and cascades to their sessions).
// Returns ErrNotFound if user doesn't exist, ErrLastAdmin if last admin.
func (s *Store) DeleteUser(username string) error {
	var role string
	err := s.db.QueryRow(`SELECT role FROM users WHERE username = ?`, username).Scan(&role)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("auth: delete user lookup: %w", err)
	}
	if Role(role) == RoleAdmin {
		var count int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'admin'`).Scan(&count); err != nil {
			return fmt.Errorf("auth: count admins: %w", err)
		}
		if count <= 1 {
			return ErrLastAdmin
		}
	}
	_, err = s.db.Exec(`DELETE FROM users WHERE username = ?`, username)
	if err != nil {
		return fmt.Errorf("auth: delete user: %w", err)
	}
	return nil
}

// UpdatePassword changes the password for an existing user.
func (s *Store) UpdatePassword(username, password string) error {
	res, err := s.db.Exec(`UPDATE users SET password = ? WHERE username = ?`, password, username)
	if err != nil {
		return fmt.Errorf("auth: update password: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// PruneExpired removes all sessions whose expires_at is in the past.
func (s *Store) PruneExpired() {
	s.db.Exec(`DELETE FROM sessions WHERE expires_at <= datetime('now')`) //nolint:errcheck
}

// generateToken returns a cryptographically random 64-character hex string.
func generateToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("auth: failed to generate random token: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// isUniqueViolation reports whether err is a SQLite UNIQUE constraint error.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
