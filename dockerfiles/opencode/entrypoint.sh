#!/bin/bash
# Entrypoint for OpenCode container with ttyd + tmux
# TTYD_BASE_PATH is set by the Go server when creating the container
# Configure your AI provider API keys via the environment variables panel.

exec ttyd -W -p ${TTYD_PORT:-7681} --base-path "${TTYD_BASE_PATH:-/}" \
    tmux new-session -A -s opencode-session \
    "cd /workspace && opencode"
