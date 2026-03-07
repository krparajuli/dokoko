#!/usr/bin/env bash
# migrate.sh — apply or rollback SQL migrations on dokokostore.db
#
# Usage:
#   ./migrate.sh [up|down] [DB_PATH]
#
#   up   (default) — apply all pending .up.sql migrations in version order
#   down            — roll back the highest applied migration using its .down.sql
#
# DB_PATH defaults to database/dokokostore.db relative to the repo root.
# Applied versions are tracked in the schema_migrations table, which this
# script owns (individual migration files do not touch it).

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
MIGRATIONS_DIR="${REPO_ROOT}/database/migrations"

DIRECTION="${1:-up}"
DB_PATH="${2:-${REPO_ROOT}/database/dokokostore.db}"

if [[ "${DIRECTION}" != "up" && "${DIRECTION}" != "down" ]]; then
    echo "usage: migrate.sh [up|down] [DB_PATH]" >&2
    exit 1
fi

if ! command -v sqlite3 &>/dev/null; then
    echo "error: sqlite3 not found — install SQLite to continue" >&2
    exit 1
fi

mkdir -p "$(dirname "${DB_PATH}")"

# Bootstrap schema_migrations so it always exists before any query.
sqlite3 "${DB_PATH}" <<'SQL'
CREATE TABLE IF NOT EXISTS schema_migrations (
    version     INTEGER PRIMARY KEY,
    applied_at  TEXT    NOT NULL  -- RFC3339 UTC
);
SQL

echo "database:   ${DB_PATH}"
echo "direction:  ${DIRECTION}"
echo "migrations: ${MIGRATIONS_DIR}"
echo ""

# ── up ────────────────────────────────────────────────────────────────────────

if [[ "${DIRECTION}" == "up" ]]; then
    applied=0
    skipped=0

    for up_file in $(ls "${MIGRATIONS_DIR}"/*.up.sql 2>/dev/null | sort); do
        filename="$(basename "${up_file}")"
        version_str="${filename%%_*}"
        version=$((10#${version_str}))

        already_applied=$(sqlite3 "${DB_PATH}" \
            "SELECT COUNT(*) FROM schema_migrations WHERE version = ${version};")

        if [ "${already_applied}" -gt 0 ]; then
            echo "  skip  v${version_str}  ${filename}"
            skipped=$((skipped + 1))
            continue
        fi

        echo "  apply v${version_str}  ${filename}"
        sqlite3 "${DB_PATH}" < "${up_file}"

        sqlite3 "${DB_PATH}" \
            "INSERT INTO schema_migrations (version, applied_at)
             VALUES (${version}, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'));"

        applied=$((applied + 1))
    done

    echo ""
    echo "done: ${applied} applied, ${skipped} skipped"
    exit 0
fi

# ── down ──────────────────────────────────────────────────────────────────────

# Find the highest applied version.
current=$(sqlite3 "${DB_PATH}" \
    "SELECT COALESCE(MAX(version), 0) FROM schema_migrations;")

if [ "${current}" -eq 0 ]; then
    echo "nothing to roll back — schema_migrations is empty"
    exit 0
fi

# Pad version to 3 digits to match filename prefix.
version_str=$(printf "%03d" "${current}")

down_file="${MIGRATIONS_DIR}/${version_str}_"*".down.sql"
# Expand glob.
down_file=$(ls ${down_file} 2>/dev/null | head -1)

if [ -z "${down_file}" ]; then
    echo "error: no .down.sql file found for version ${current}" >&2
    exit 1
fi

filename="$(basename "${down_file}")"
echo "  rollback v${version_str}  ${filename}"
sqlite3 "${DB_PATH}" < "${down_file}"

sqlite3 "${DB_PATH}" \
    "DELETE FROM schema_migrations WHERE version = ${current};"

echo ""
echo "done: rolled back v${version_str}"
