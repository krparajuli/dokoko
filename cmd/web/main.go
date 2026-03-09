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
	"strings"
	"syscall"
	"time"

	"dokoko.ai/dokoko/cmd/web/server"
	authpkg "dokoko.ai/dokoko/internal/auth"
	imagecfg "dokoko.ai/dokoko/internal/imageconfig"
	dockermanager "dokoko.ai/dokoko/internal/docker/manager"
	"dokoko.ai/dokoko/pkg/logger"
)

func main() {
	addr      := flag.String("addr", ":8888", "HTTP server address")
	logLvl    := flag.String("log", "info", "log level: error,warn,info,debug,trace")
	uiDir     := flag.String("ui-dir", "", "path to built UI files (ui/dist); auto-detected if empty")
	adminUser     := flag.String("admin-user", "admin", "admin username")
	adminPass     := flag.String("admin-password", "admin", "admin password")
	userName      := flag.String("user-name", "user", "default non-admin username")
	userPass      := flag.String("user-password", "password", "default non-admin password")
	allowedImages := flag.String("allowed-images", "claudewebd,gemini,codex,opencode", "comma-separated catalog IDs available to non-admin users (empty = all)")
	configFile    := flag.String("config", "dokoko.yaml", "path to YAML config file (image env-var schemas, etc.)")
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

	if *adminUser == "admin" && *adminPass == "admin" {
		log.Warn("using default admin credentials — set --admin-user and --admin-password in production")
	}
	if *userPass == "password" {
		log.Warn("using default user credentials — set --user-name and --user-password in production")
	}
	users := []authpkg.User{
		{Username: *adminUser, Password: *adminPass, Role: authpkg.RoleAdmin},
		{Username: *userName, Password: *userPass, Role: authpkg.RoleUser},
	}

	var allowed []string
	if *allowedImages != "" {
		for _, s := range strings.Split(*allowedImages, ",") {
			if t := strings.TrimSpace(s); t != "" {
				allowed = append(allowed, t)
			}
		}
	}

	imgCfg, err := imagecfg.Load(*configFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}
	log.Info("loaded image config from %s (%d image var schema(s))", *configFile, len(imgCfg.ImageVars))

	srv := server.New(mgr, log, *addr, *uiDir, users, allowed, imgCfg)
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
