BIN := /tmp/dokoko-tui
TUI := ./cmd/cli/dokoko

.PHONY: build run tui

build:
	go build -o $(BIN) $(TUI)

run: build
	$(BIN)

tui: run
