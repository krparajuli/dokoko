package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	dockermanager "dokoko.ai/dokoko/internal/docker/manager"
	"dokoko.ai/dokoko/pkg/graceful"
	"dokoko.ai/dokoko/pkg/logger"
	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	logFile, err := os.OpenFile("/tmp/dokoko-tui.log",
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open log file: %v\n", err)
		os.Exit(1)
	}
	defer logFile.Close()

	lb := newLogBuf(500)
	log := logger.New(logger.LevelInfo)
	log.SetOutput(io.MultiWriter(logFile, lb))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	mgr, err := dockermanager.New(ctx, log)
	cancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "docker connect: %v\n", err)
		os.Exit(1)
	}
	p := tea.NewProgram(newModel(mgr, lb), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tui: %v\n", err)
		mgr.Close() //nolint:errcheck
		os.Exit(1)
	}

	// ── Print service state ───────────────────────────────────────────────────
	fmt.Println("\nService state:")
	graceful.PrintState("Images",          "active", log)
	graceful.PrintState("Containers",      "active", log)
	graceful.PrintState("Builds",          "active", log)
	graceful.PrintState("Networks",        "active", log)
	graceful.PrintState("Volumes",         "active", log)
	graceful.PrintState("Port Proxy",      "active", log)
	graceful.PrintState("Web Containers",  "active", log)
	graceful.PrintState("Docker Manager",  "connected", log)
	fmt.Println()

	// ── Ordered shutdown ──────────────────────────────────────────────────────
	graceful.Service("Docker Manager", log, func() {
		if err := mgr.Close(); err != nil {
			log.Error("Docker Manager shutdown error: %v", err)
		}
	})
	graceful.Done(log)
}
