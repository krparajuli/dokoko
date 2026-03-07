#!/bin/bash
# Entrypoint for Claude Code container with ttyd + tmux
# TTYD_BASE_PATH is set by the Go server when creating the container

# Start ttyd with tmux session that runs Claude Code in workspace
exec ttyd -W -p ${TTYD_PORT:-7681} --base-path "${TTYD_BASE_PATH:-/}" \
    tmux new-session -A -s claude-session \
    "cd /workspace && claude --dangerously-skip-permissions"
