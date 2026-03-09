// Package main is the entry point for the dokoko web server.
// It connects to Docker, starts the HTTP server, and serves the web UI.
package main

import (
	"bufio"
	"context"
	"database/sql"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"dokoko.ai/dokoko/cmd/web/server"
	authpkg "dokoko.ai/dokoko/internal/auth"
	imagecfg "dokoko.ai/dokoko/internal/imageconfig"
	dockermanager "dokoko.ai/dokoko/internal/docker/manager"
	"dokoko.ai/dokoko/pkg/graceful"
	"dokoko.ai/dokoko/pkg/logger"
	_ "modernc.org/sqlite"
)

func main() {
	// Load .env before flag.Parse so env vars act as defaults.
	// Priority (highest → lowest): CLI flags > real env vars > .env file > hardcoded defaults.
	loadDotenv(".env")

	addr          := flag.String("addr",           envOr("DOKOKO_ADDR", ":8888"),                                       "HTTP server address")
	logLvl        := flag.String("log",            envOr("DOKOKO_LOG_LEVEL", "info"),                                   "log level: error,warn,info,debug,trace")
	uiDir         := flag.String("ui-dir",         envOr("DOKOKO_UI_DIR", ""),                                          "path to built UI files (ui/dist); auto-detected if empty")
	dbPath        := flag.String("db",             envOr("DOKOKO_DB_PATH", "dokoko.db"),                                "path to SQLite database file")
	adminUser     := flag.String("admin-user",     envOr("DOKOKO_ADMIN_USER", "admin"),                                 "admin username (seeded on first run)")
	adminPass     := flag.String("admin-password", envOr("DOKOKO_ADMIN_PASSWORD", "admin"),                             "admin password (seeded on first run)")
	allowedImages := flag.String("allowed-images", envOr("DOKOKO_ALLOWED_IMAGES", "claudewebd,gemini,codex,opencode"), "comma-separated catalog IDs available to non-admin users (empty = all)")
	configFile    := flag.String("config",         envOr("DOKOKO_CONFIG", "dokoko.yaml"),                               "path to YAML config file (image env-var schemas, etc.)")
	flag.Parse()

	log := logger.New(parseLevel(*logLvl))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ── Open / create SQLite database ────────────────────────────────────────
	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open database %s: %v\n", *dbPath, err)
		os.Exit(1)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect to database %s: %v\n", *dbPath, err)
		os.Exit(1)
	}

	// ── Run schema migrations ────────────────────────────────────────────────
	if err := authpkg.RunMigrations(db); err != nil {
		fmt.Fprintf(os.Stderr, "failed to run migrations: %v\n", err)
		os.Exit(1)
	}

	// ── Seed admin on first run ──────────────────────────────────────────────
	store := authpkg.New(db)
	var userCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&userCount); err != nil {
		fmt.Fprintf(os.Stderr, "failed to count users: %v\n", err)
		os.Exit(1)
	}
	if userCount == 0 {
		if *adminUser == "admin" && *adminPass == "admin" {
			log.Warn("seeding default admin credentials — set DOKOKO_ADMIN_USER and DOKOKO_ADMIN_PASSWORD in production")
		}
		if err := store.SeedUser(authpkg.User{
			Username: *adminUser,
			Password: *adminPass,
			Role:     authpkg.RoleAdmin,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "failed to seed admin user: %v\n", err)
			os.Exit(1)
		}
		log.Info("seeded admin user %q", *adminUser)
	}

	mgr, err := dockermanager.New(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect to Docker: %v\n", err)
		os.Exit(1)
	}
	defer mgr.Close()

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

	srv := server.New(mgr, log, *addr, *uiDir, db, allowed, imgCfg)
	log.SetOutput(srv.LogWriter())

	go func() {
		if err := srv.Start(); err != nil && err != http.ErrServerClosed {
			log.Error("server error: %v", err)
			cancel()
		}
	}()

	fmt.Printf("dokoko web UI → http://%s\n", *addr)

	// Block until the server itself errors or the user confirms Ctrl-C twice.
	select {
	case <-ctx.Done():
		// server error triggered cancel
	case <-graceful.ExitCh("Press Ctrl-C again to exit."):
		// user confirmed
	}

	// ── Print service state ──────────────────────────────────────────────────
	fmt.Println("\nService state:")
	graceful.PrintState("HTTP Server",    "running ("+*addr+")", log)
	graceful.PrintState("Docker Manager", "connected",           log)
	graceful.PrintState("Web Containers", "active",              log)
	graceful.PrintState("Port Proxy",     "active",              log)
	fmt.Println()

	// ── Ordered shutdown ─────────────────────────────────────────────────────
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	graceful.Service("HTTP Server", log, func() {
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Error("HTTP Server shutdown error: %v", err)
		}
	})
	graceful.Service("Docker Manager", log, func() {
		if err := mgr.Close(); err != nil {
			log.Error("Docker Manager shutdown error: %v", err)
		}
	})

	graceful.Done(log)
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

// envOr returns the value of the named environment variable, or fallback when
// the variable is unset or empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// loadDotenv reads a KEY=VALUE file and populates the process environment for
// any key that is not already set.  Real environment variables always win.
// Lines starting with '#' and blank lines are ignored.  Values may optionally
// be wrapped in single or double quotes, which are stripped.
func loadDotenv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return // missing .env is fine
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])

		// Strip inline comment (unquoted # after value)
		if len(val) > 0 && val[0] != '"' && val[0] != '\'' {
			if ci := strings.Index(val, " #"); ci >= 0 {
				val = strings.TrimSpace(val[:ci])
			}
		}

		// Strip surrounding quotes
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') ||
				(val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}

		if os.Getenv(key) == "" {
			os.Setenv(key, val) //nolint:errcheck
		}
	}
}
