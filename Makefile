BIN     := /tmp/dokoko-tui
TUI     := ./cmd/cli/dokoko
WEB_BIN := /tmp/dokoko-web
WEB     := ./cmd/web
UI_DIR  := cmd/web/ui

.PHONY: build run tui web-ui web-build web web-dev

# ── TUI ───────────────────────────────────────────────────────────────────────

build:
	go build -o $(BIN) $(TUI)

run: build
	$(BIN)

tui: run

# ── Web app ───────────────────────────────────────────────────────────────────

## web-ui: install deps and build the TypeScript/React frontend
web-ui:
	cd $(UI_DIR) && npm install && npm run build

## web-build: build the Go web server binary (builds UI first)
web-build: web-ui
	go build -o $(WEB_BIN) $(WEB)

## web: run the web server (builds UI first, then starts server)
web: web-build
	$(WEB_BIN)

## web-dev: start Vite dev server (proxies /api to :8080) — run the Go server separately
web-dev:
	cd $(UI_DIR) && npm run dev
