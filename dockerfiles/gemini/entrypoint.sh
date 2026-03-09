#!/bin/bash
# Entrypoint for Gemini CLI container with ttyd + tmux
# TTYD_BASE_PATH is set by the Go server when creating the container
# Set GEMINI_API_KEY via the environment variables panel before starting.

exec ttyd -W -p ${TTYD_PORT:-7681} --base-path "${TTYD_BASE_PATH:-/}" \
    tmux new-session -A -s gemini-session \
    "cd /workspace && gemini"
