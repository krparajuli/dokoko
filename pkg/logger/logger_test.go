package logger

import (
	"bytes"
	"strings"
	"testing"
)

func TestLevelFiltering(t *testing.T) {
	tests := []struct {
		loggerLevel Level
		logAt       Level
		wantOutput  bool
	}{
		{LevelSilent, LevelError, false},
		{LevelSilent, LevelTrace, false},
		{LevelError, LevelError, true},
		{LevelError, LevelWarn, false},
		{LevelWarn, LevelWarn, true},
		{LevelWarn, LevelLowTrace, false},
		{LevelWarn, LevelInfo, false},
		{LevelLowTrace, LevelWarn, true},
		{LevelLowTrace, LevelLowTrace, true},
		{LevelLowTrace, LevelInfo, false},
		{LevelInfo, LevelLowTrace, true},
		{LevelInfo, LevelInfo, true},
		{LevelInfo, LevelDebug, false},
		{LevelDebug, LevelDebug, true},
		{LevelDebug, LevelTrace, false},
		{LevelTrace, LevelTrace, true},
		{LevelTrace, LevelDebug, true},
		{LevelTrace, LevelError, true},
	}

	for _, tt := range tests {
		var buf bytes.Buffer
		l := New(tt.loggerLevel)
		l.SetOutput(&buf)

		switch tt.logAt {
		case LevelTrace:
			l.Trace("msg")
		case LevelDebug:
			l.Debug("msg")
		case LevelInfo:
			l.Info("msg")
		case LevelLowTrace:
			l.LowTrace("msg")
		case LevelWarn:
			l.Warn("msg")
		case LevelError:
			l.Error("msg")
		}

		got := buf.Len() > 0
		if got != tt.wantOutput {
			t.Errorf("logger level=%v, log at=%v: got output=%v, want=%v",
				tt.loggerLevel, tt.logAt, got, tt.wantOutput)
		}
	}
}

func TestLevelLabelsInOutput(t *testing.T) {
	levels := []struct {
		level Level
		label string
		fn    func(*Logger, string)
	}{
		{LevelTrace, "[TRACE]", func(l *Logger, m string) { l.Trace(m) }},
		{LevelLowTrace, "[LTRACE]", func(l *Logger, m string) { l.LowTrace(m) }},
		{LevelDebug, "[DEBUG]", func(l *Logger, m string) { l.Debug(m) }},
		{LevelInfo, "[INFO]", func(l *Logger, m string) { l.Info(m) }},
		{LevelWarn, "[WARN]", func(l *Logger, m string) { l.Warn(m) }},
		{LevelError, "[ERROR]", func(l *Logger, m string) { l.Error(m) }},
	}

	for _, tt := range levels {
		var buf bytes.Buffer
		l := New(tt.level)
		l.SetOutput(&buf)
		tt.fn(l, "hello")

		if !strings.Contains(buf.String(), tt.label) {
			t.Errorf("expected label %q in output %q", tt.label, buf.String())
		}
	}
}

func TestTraceIncludesCallerPath(t *testing.T) {
	var buf bytes.Buffer
	l := New(LevelTrace)
	l.SetOutput(&buf)
	l.Trace("traced")

	out := buf.String()

	// Should contain the " > "-separated path and a "|" separator.
	if !strings.Contains(out, " > ") {
		t.Errorf("trace output missing ' > ' path separator: %q", out)
	}
	if !strings.Contains(out, " | ") {
		t.Errorf("trace output missing ' | ' caller/message separator: %q", out)
	}
	// The caller is this test function; its name must appear in the path.
	if !strings.Contains(out, "TestTraceIncludesCallerPath") {
		t.Errorf("trace output missing function name: %q", out)
	}
}

func TestLowTraceCallerPathIsShort(t *testing.T) {
	var buf bytes.Buffer
	l := New(LevelLowTrace)
	l.SetOutput(&buf)
	l.LowTrace("low traced")

	out := buf.String()

	if !strings.Contains(out, "[LTRACE]") {
		t.Errorf("missing [LTRACE] label: %q", out)
	}
	if !strings.Contains(out, " | ") {
		t.Errorf("missing ' | ' separator: %q", out)
	}

	// Extract the caller path between [LTRACE] and |
	// Format: "... [LTRACE] <path> | <msg>"
	parts := strings.SplitN(out, " | ", 2)
	if len(parts) < 2 {
		t.Fatalf("unexpected format: %q", out)
	}
	pathPart := parts[0]
	segments := strings.Split(strings.TrimSpace(pathPart[strings.LastIndex(pathPart, "]")+1:]), " > ")

	// LowTrace must emit exactly 2 path segments.
	if len(segments) != 2 {
		t.Errorf("LowTrace path should have 2 segments, got %d: %v (full output: %q)",
			len(segments), segments, out)
	}
}

func TestNonTraceHasNoCallerPath(t *testing.T) {
	var buf bytes.Buffer
	l := New(LevelDebug)
	l.SetOutput(&buf)
	l.Debug("debug msg")

	if strings.Contains(buf.String(), " > ") {
		t.Errorf("non-trace output should not contain caller path: %q", buf.String())
	}
}

func TestFormatArgs(t *testing.T) {
	var buf bytes.Buffer
	l := New(LevelInfo)
	l.SetOutput(&buf)
	l.Info("value is %d and %s", 42, "hello")

	if !strings.Contains(buf.String(), "value is 42 and hello") {
		t.Errorf("formatted message not in output: %q", buf.String())
	}
}

func TestFormatFuncPath(t *testing.T) {
	tests := []struct {
		input  string
		module string
		want   string
	}{
		{
			"dokoko.ai/dokoko/internal/docker/ops.New",
			"dokoko.ai/dokoko",
			"internal > docker > ops > New",
		},
		{
			"dokoko.ai/dokoko/internal/docker/ops.(*Manager).New",
			"dokoko.ai/dokoko",
			"internal > docker > ops > New",
		},
		{
			"dokoko.ai/dokoko/pkg/logger.(*Logger).Trace",
			"dokoko.ai/dokoko",
			"pkg > logger > Trace",
		},
		{
			"main.SomeFunc",
			"",
			"main > SomeFunc",
		},
		{
			"net/http.(*Client).Do",
			"",
			"net > http > Do",
		},
		{
			"github.com/user/project/internal/docker/ops.New",
			"github.com/user/project",
			"internal > docker > ops > New",
		},
	}

	for _, tt := range tests {
		got := formatFuncPath(tt.input, tt.module)
		if got != tt.want {
			t.Errorf("formatFuncPath(%q, %q)\n  got  %q\n  want %q", tt.input, tt.module, got, tt.want)
		}
	}
}

func TestSetLevel(t *testing.T) {
	var buf bytes.Buffer
	l := New(LevelSilent)
	l.SetOutput(&buf)

	l.Info("silent")
	if buf.Len() != 0 {
		t.Error("expected no output at LevelSilent")
	}

	l.SetLevel(LevelInfo)
	l.Info("now visible")
	if !strings.Contains(buf.String(), "now visible") {
		t.Errorf("expected output after SetLevel, got %q", buf.String())
	}
}
