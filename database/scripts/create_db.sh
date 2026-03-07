#!/usr/bin/env bash
# create_db.sh — create dokokostore.db and apply all migrations.
#
# Usage:
#   ./create_db.sh [DB_PATH]
#
# If DB_PATH already exists the script exits early so it is safe to call
# idempotently (e.g. from a startup hook).

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
DB_PATH="${1:-${REPO_ROOT}/database/dokokostore.db}"

if [ -f "${DB_PATH}" ]; then
    echo "database already exists: ${DB_PATH}"
    echo "run migrate.sh to apply pending migrations, or reset_db.sh to start fresh"
    exit 0
fi

echo "creating database: ${DB_PATH}"
"${SCRIPT_DIR}/migrate.sh" "${DB_PATH}"
echo "database created: ${DB_PATH}"
