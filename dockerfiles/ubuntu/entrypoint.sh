#!/bin/bash
# Entrypoint for Ubuntu container with ttyd + tmux
# TTYD_BASE_PATH is set by the Go server when creating the container

exec ttyd -W -p ${TTYD_PORT:-7681} --base-path "${TTYD_BASE_PATH:-/}" tmux new-session -A -s main
