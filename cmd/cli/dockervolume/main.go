package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"dokoko.ai/dokoko/internal/docker"
	dockervolumeactor "dokoko.ai/dokoko/internal/docker/volumes/actor"
	dockervolumeops "dokoko.ai/dokoko/internal/docker/volumes/ops"
	dockervolumestate "dokoko.ai/dokoko/internal/docker/volumes/state"
	"dokoko.ai/dokoko/pkg/logger"
	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	// TUI owns stdout/stderr, so route all log output to a file.
	logFile, err := os.OpenFile("/tmp/dokoko-vol-tui.log",
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open log file: %v\n", err)
		os.Exit(1)
	}
	defer logFile.Close()

	log := logger.New(logger.LevelDebug)
	log.SetOutput(logFile)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	conn, err := docker.New(ctx, log)
	cancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "docker connect: %v\n", err)
		os.Exit(1)
	}

	ops := dockervolumeops.New(conn, log)
	st := dockervolumestate.New(log)
	act := dockervolumeactor.New(ops, st, log, nil)
	defer act.Close()

	p := tea.NewProgram(newModel(act, st), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tui: %v\n", err)
		os.Exit(1)
	}
}
