#!/bin/sh
# Entrypoint for nginx container with ttyd + nginx

# Start nginx in the background
nginx

# Start ttyd in the foreground with tmux
exec ttyd -W -p ${TTYD_PORT:-7681} --base-path "${TTYD_BASE_PATH:-/}" tmux new-session -A -s main
