#!/usr/bin/env bash
# reset_db.sh — drop and recreate dokokostore.db from scratch.
#
# Usage:
#   ./reset_db.sh [DB_PATH]
#
# WARNING: all data is permanently deleted.  Use only in development.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
DB_PATH="${1:-${REPO_ROOT}/database/dokokostore.db}"

if [ -f "${DB_PATH}" ]; then
    echo "dropping: ${DB_PATH}"
    rm -f "${DB_PATH}"
fi

echo "recreating database: ${DB_PATH}"
"${SCRIPT_DIR}/migrate.sh" "${DB_PATH}"
echo "reset complete: ${DB_PATH}"
