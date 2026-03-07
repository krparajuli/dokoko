package logger

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"time"
)

// modulePrefix holds the current module path (e.g. "dokoko.ai/dokoko") so it
// can be stripped from caller paths at trace time.
var modulePrefix string

func init() {
	if info, ok := debug.ReadBuildInfo(); ok {
		modulePrefix = info.Main.Path
	}
}

// Level represents a log verbosity level. Higher values are more verbose.
type Level int

const (
	LevelSilent   Level = iota // no output
	LevelError                 // errors only
	LevelWarn                  // warnings and above
	LevelLowTrace              // warn + caller path (pkg > Func), less verbose than Info
	LevelInfo                  // informational and above
	LevelDebug                 // debug and above
	LevelTrace                 // everything, full caller path (internal > docker > ops > Func)
)

var levelLabels = map[Level]string{
	LevelSilent:   "SILENT",
	LevelError:    "ERROR",
	LevelWarn:     "WARN",
	LevelLowTrace: "LTRACE",
	LevelInfo:     "INFO",
	LevelDebug:    "DEBUG",
	LevelTrace:    "TRACE",
}

// Logger is a leveled logger. At LevelTrace it emits the caller path in the
// form "pkg > subpkg > ... > FuncName" alongside every message.
type Logger struct {
	level  Level
	output io.Writer
}

// New returns a Logger writing to stdout at the given level.
func New(level Level) *Logger {
	return &Logger{
		level:  level,
		output: os.Stdout,
	}
}

// SetOutput redirects all log output to w.
func (l *Logger) SetOutput(w io.Writer) { l.output = w }

// SetLevel changes the active log level.
func (l *Logger) SetLevel(level Level) { l.level = level }

// Level returns the current log level.
func (l *Logger) Level() Level { return l.level }

// Trace logs at TRACE level and prepends the full caller path, e.g.
//
//	2006-01-02 15:04:05.000 [TRACE] internal > docker > ops > New | message
func (l *Logger) Trace(msg string, args ...any) { l.emit(LevelTrace, 2, msg, args...) }

// LowTrace logs at LTRACE level and prepends a short caller path (last 2 segments), e.g.
//
//	2006-01-02 15:04:05.000 [LTRACE] ops > New | message
func (l *Logger) LowTrace(msg string, args ...any) { l.emit(LevelLowTrace, 2, msg, args...) }

// Debug logs at DEBUG level.
func (l *Logger) Debug(msg string, args ...any) { l.emit(LevelDebug, 2, msg, args...) }

// Info logs at INFO level.
func (l *Logger) Info(msg string, args ...any) { l.emit(LevelInfo, 2, msg, args...) }

// Warn logs at WARN level.
func (l *Logger) Warn(msg string, args ...any) { l.emit(LevelWarn, 2, msg, args...) }

// Error logs at ERROR level.
func (l *Logger) Error(msg string, args ...any) { l.emit(LevelError, 2, msg, args...) }

// emit writes a log line if the logger's level is at or above level.
// skip is the number of call frames above emit() to attribute the log line to.
func (l *Logger) emit(level Level, skip int, msg string, args ...any) {
	if l.level == LevelSilent || l.level < level {
		return
	}

	if len(args) > 0 {
		msg = fmt.Sprintf(msg, args...)
	}

	ts := time.Now().Format("2006-01-02 15:04:05.000")
	label := levelLabels[level]

	switch level {
	case LevelTrace:
		caller := callerPath(skip+1, 0) // full path: internal > docker > ops > New
		fmt.Fprintf(l.output, "%s [%s] %s | %s\n", ts, label, caller, msg)
	case LevelLowTrace:
		caller := callerPath(skip+1, 2) // short path: ops > New
		fmt.Fprintf(l.output, "%s [%s] %s | %s\n", ts, label, caller, msg)
	default:
		fmt.Fprintf(l.output, "%s [%s] %s\n", ts, label, msg)
	}
}

// callerPath returns the caller's location as a " > "-separated path.
// skip counts frames above callerPath itself (0 = callerPath's direct caller).
// maxDepth limits the number of trailing segments kept; 0 means keep all.
func callerPath(skip, maxDepth int) string {
	pc, _, _, ok := runtime.Caller(skip)
	if !ok {
		return "unknown"
	}

	fn := runtime.FuncForPC(pc)
	if fn == nil {
		return "unknown"
	}

	path := formatFuncPath(fn.Name(), modulePrefix)
	if maxDepth > 0 {
		parts := strings.Split(path, " > ")
		if len(parts) > maxDepth {
			parts = parts[len(parts)-maxDepth:]
		}
		return strings.Join(parts, " > ")
	}
	return path
}

// formatFuncPath converts a fully-qualified Go function name into a
// human-readable path using " > " as separator.  module is the current
// module path (e.g. "dokoko.ai/dokoko") and is stripped as a prefix.
//
// Examples (module = "dokoko.ai/dokoko"):
//
//	"dokoko.ai/dokoko/internal/docker/ops.(*Manager).New" → "internal > docker > ops > New"
//	"dokoko.ai/dokoko/pkg/logger.(*Logger).Trace"         → "pkg > logger > Trace"
//	"main.SomeFunc"                                        → "main > SomeFunc"
//	"net/http.(*Client).Do"                                → "net > http > Do"
func formatFuncPath(fullName, module string) string {
	// Split at the last "/" to separate the slash-delimited package path from
	// the dot-delimited "<package>.<symbol>" tail.
	lastSlash := strings.LastIndex(fullName, "/")

	var pathSegments []string
	var tail string

	if lastSlash >= 0 {
		pathSegments = strings.Split(fullName[:lastSlash], "/")
		tail = fullName[lastSlash+1:]
	} else {
		tail = fullName
	}

	// tail is e.g. "ops.New" or "ops.(*Manager).New"
	// The package name ends at the first ".".
	dotIdx := strings.Index(tail, ".")
	if dotIdx < 0 {
		return tail // bare identifier, nothing to reformat
	}

	pkg := tail[:dotIdx]      // e.g. "ops"
	symbol := tail[dotIdx+1:] // e.g. "New" or "(*Manager).New"

	// Strip receiver type: "(*Manager).New" → "New"
	if i := strings.LastIndex(symbol, "."); i >= 0 {
		symbol = symbol[i+1:]
	}

	// Build the full segment list: path parts + package name.
	allParts := append(pathSegments, pkg)

	// Determine how many leading segments belong to the module root and
	// should be hidden.
	start := moduleRootLen(allParts, module)

	relevant := append(allParts[start:], symbol)
	return strings.Join(relevant, " > ")
}

// moduleRootLen returns the number of leading path segments that belong to the
// module root and should be stripped from the caller path.
//
// If module is non-empty, it is split on "/" and that exact count is used.
// Otherwise a heuristic is applied: when the first segment looks like a domain
// (contains ".") assume "domain/org/repo" = 3 segments; for a two-part module
// like "example.com/app" the exact match path handles it automatically.
func moduleRootLen(parts []string, module string) int {
	if module != "" {
		moduleSegments := strings.Split(module, "/")
		n := len(moduleSegments)
		if n > len(parts) {
			n = len(parts)
		}
		// Verify the prefix actually matches before trusting it.
		match := true
		for i := 0; i < n; i++ {
			if parts[i] != moduleSegments[i] {
				match = false
				break
			}
		}
		if match {
			return n
		}
	}

	// Fallback heuristic: domain-prefixed module paths.
	if len(parts) > 0 && strings.Contains(parts[0], ".") {
		n := 3 // github.com/user/repo style
		if n > len(parts) {
			n = len(parts)
		}
		return n
	}

	return 0
}
