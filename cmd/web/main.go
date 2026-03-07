// Package main is the entry point for the dokoko web server.
// It connects to Docker, starts the HTTP server, and serves the web UI.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"dokoko.ai/dokoko/cmd/web/server"
	dockermanager "dokoko.ai/dokoko/internal/docker/manager"
	"dokoko.ai/dokoko/pkg/logger"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP server address")
	logLvl := flag.String("log", "info", "log level: error,warn,info,debug,trace")
	uiDir := flag.String("ui-dir", "", "path to built UI files (ui/dist); auto-detected if empty")
	flag.Parse()

	log := logger.New(parseLevel(*logLvl))

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	mgr, err := dockermanager.New(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect to Docker: %v\n", err)
		os.Exit(1)
	}
	defer mgr.Close()

	srv := server.New(mgr, log, *addr, *uiDir)
	log.SetOutput(srv.LogWriter())

	go func() {
		if err := srv.Start(); err != nil && err != http.ErrServerClosed {
			log.Error("server error: %v", err)
			cancel()
		}
	}()

	fmt.Printf("dokoko web UI → http://%s\n", *addr)
	<-ctx.Done()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown error: %v", err)
	}
}

func parseLevel(s string) logger.Level {
	switch s {
	case "error":
		return logger.LevelError
	case "warn":
		return logger.LevelWarn
	case "debug":
		return logger.LevelDebug
	case "trace":
		return logger.LevelTrace
	default:
		return logger.LevelInfo
	}
}
