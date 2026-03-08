# ── Config ────────────────────────────────────────────────────────────────────

MODULE   := dokoko.ai/dokoko
UI_DIR   := cmd/web/ui

BIN_DIR  := bin
TUI_BIN  := $(BIN_DIR)/dokoko
WEB_BIN  := $(BIN_DIR)/dokoko-web

TUI_PKG  := ./cmd/cli/dokoko
WEB_PKG  := ./cmd/web

GO       := go
GOFLAGS  :=

# ── Phony targets ─────────────────────────────────────────────────────────────

.PHONY: help \
        build build-tui build-web build-all \
        run tui web \
        web-ui ui-install ui-build web-dev \
        test test-race test-web test-cover \
        vet lint fmt \
        clean clean-ui clean-bin

# ── Default: help ─────────────────────────────────────────────────────────────

help: ## Show this help
	@awk 'BEGIN{FS=":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n\nTargets:\n"} \
	  /^[a-zA-Z_-]+:.*##/ {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# ── Build ─────────────────────────────────────────────────────────────────────

$(BIN_DIR):
	mkdir -p $(BIN_DIR)

build: build-tui ## Build the TUI binary → bin/dokoko

build-tui: $(BIN_DIR) ## Build the TUI binary → bin/dokoko
	$(GO) build $(GOFLAGS) -o $(TUI_BIN) $(TUI_PKG)

build-web: ui-build $(BIN_DIR) ## Build the web server (builds UI first) → bin/dokoko-web
	$(GO) build $(GOFLAGS) -o $(WEB_BIN) $(WEB_PKG)

build-all: build-tui build-web ## Build all binaries

# ── Run ───────────────────────────────────────────────────────────────────────

run: build-tui ## Build and run the TUI
	$(TUI_BIN)

tui: run ## Alias for run

web: build-web ## Build and run the web server
	$(WEB_BIN)

# ── UI ────────────────────────────────────────────────────────────────────────

ui-install: ## Install Node.js dependencies
	cd $(UI_DIR) && npm install

ui-build: ## Build the React/TypeScript frontend (skips npm install)
	cd $(UI_DIR) && npm run build

web-ui: ui-install ui-build ## Install deps and build the frontend

web-dev: ## Start the Vite dev server (run the Go server separately on :8080)
	cd $(UI_DIR) && npm run dev

# ── Test ──────────────────────────────────────────────────────────────────────

test: ## Run all Go tests
	$(GO) test ./... -timeout 120s

test-race: ## Run all Go tests with the race detector
	$(GO) test ./... -race -timeout 120s

test-web: ## Run web server tests only
	$(GO) test ./cmd/web/server/... -race -timeout 30s

test-cover: ## Run tests and print a coverage summary
	$(GO) test ./... -coverprofile=coverage.out -timeout 120s
	$(GO) tool cover -func=coverage.out | tail -1
	@rm -f coverage.out

# ── Quality ───────────────────────────────────────────────────────────────────

vet: ## Run go vet across all packages
	$(GO) vet ./...

fmt: ## Format all Go source files with gofmt
	gofmt -w .

lint: vet ## Run go vet (extend with golangci-lint if installed)
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run || \
	  echo "golangci-lint not found — skipping (only go vet ran)"

# ── Clean ─────────────────────────────────────────────────────────────────────

clean: clean-bin clean-ui ## Remove all build artefacts

clean-bin: ## Remove compiled binaries
	rm -rf $(BIN_DIR)

clean-ui: ## Remove the frontend dist directory
	rm -rf $(UI_DIR)/dist
