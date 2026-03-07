package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"dokoko.ai/dokoko/internal/docker"
	dockerimageactor "dokoko.ai/dokoko/internal/docker/images/actor"
	dockerimageops "dokoko.ai/dokoko/internal/docker/images/ops"
	dockerimagestate "dokoko.ai/dokoko/internal/docker/images/state"
	"dokoko.ai/dokoko/pkg/logger"
	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	// TUI owns stdout/stderr, so route all log output to a file.
	logFile, err := os.OpenFile("/tmp/dokoko-tui.log",
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open log file: %v\n", err)
		os.Exit(1)
	}
	defer logFile.Close()

	log := logger.New(logger.LevelDebug)
	log.SetOutput(logFile)

	// Connect to Docker with a short timeout; once connected the individual
	// op calls carry their own contexts.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	conn, err := docker.New(ctx, log)
	cancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "docker connect: %v\n", err)
		os.Exit(1)
	}

	ops := dockerimageops.New(conn, log)
	st := dockerimagestate.New(log)
	act := dockerimageactor.New(ops, st, log, nil)
	defer act.Close()

	p := tea.NewProgram(newModel(act, st), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tui: %v\n", err)
		os.Exit(1)
	}
}
