// Package auth provides user authentication and session management.
package auth

import (
	"database/sql"
	"fmt"
)

const schemaVersion = 2

const upSQL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    applied_at TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
    username   TEXT    PRIMARY KEY,
    password   TEXT    NOT NULL,
    role       TEXT    NOT NULL DEFAULT 'user',
    created_at TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    token      TEXT    PRIMARY KEY,
    username   TEXT    NOT NULL REFERENCES users(username) ON DELETE CASCADE,
    role       TEXT    NOT NULL,
    created_at TEXT    NOT NULL,
    expires_at TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_username   ON sessions(username);
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at);
`

// RunMigrations creates the users and sessions tables if they don't exist,
// and records the migration version in schema_migrations.
func RunMigrations(db *sql.DB) error {
	if _, err := db.Exec(upSQL); err != nil {
		return fmt.Errorf("auth: run migrations: %w", err)
	}
	_, err := db.Exec(
		`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (?, datetime('now'))`,
		schemaVersion,
	)
	if err != nil {
		return fmt.Errorf("auth: record migration version: %w", err)
	}
	return nil
}
