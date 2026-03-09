#!/bin/bash
# Entrypoint for Claude Code container with ttyd + tmux
# TTYD_BASE_PATH is set by the Go server when creating the container

# Embed Nerd Font into ttyd's index page for a better terminal experience
mkdir -p /etc/ttyd
python3 - <<'PYEOF'
import base64, os, sys
woff2 = '/usr/share/fonts/nerd-fonts/JetBrainsMonoNerdFontMono-Regular.woff2'
if not os.path.exists(woff2):
    sys.exit(0)
b64 = base64.b64encode(open(woff2, 'rb').read()).decode()
open('/etc/ttyd/index.html', 'w').write(
    '<!doctype html><html lang="en"><head>'
    '<meta charset="utf-8"/><title>Terminal</title>'
    '<meta name="viewport" content="width=device-width,initial-scale=1"/>'
    '<style>'
    '@font-face{font-family:JetBrainsMonoNF;'
    "src:url('data:font/woff2;base64," + b64 + "')format('woff2');"
    'font-weight:normal;font-style:normal}'
    'body,#terminal-container{margin:0;padding:0}'
    '</style>'
    '<link rel="stylesheet" type="text/css" href="css/app.css"/>'
    '</head><body><div id="terminal-container"></div>'
    '<script src="js/app.js"></script>'
    '</body></html>'
)
PYEOF

# Start ttyd with tmux session that runs Claude Code in workspace
exec ttyd -W -p ${TTYD_PORT:-7681} --base-path "${TTYD_BASE_PATH:-/}" \
    --index /etc/ttyd/index.html \
    -t "fontFamily=JetBrainsMonoNF, monospace" \
    -t fontSize=15 \
    tmux new-session -A -s claude-session \
    "cd /workspace && claude --dangerously-skip-permissions"
