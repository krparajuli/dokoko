package server

import (
	"bufio"
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ── logBus ────────────────────────────────────────────────────────────────────

func TestLogBus_BroadcastsToSubscriber(t *testing.T) {
	bus := newLogBus()
	ch := bus.subscribe()
	defer bus.unsubscribe(ch)

	msg := "2026-01-01 00:00:00.000 [INFO] hello world"
	if _, err := fmt.Fprintln(bus, msg); err != nil {
		t.Fatalf("Write: %v", err)
	}

	select {
	case got := <-ch:
		if got != msg {
			t.Errorf("got %q, want %q", got, msg)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for broadcast")
	}
}

func TestLogBus_MultipleSubscribers(t *testing.T) {
	bus := newLogBus()
	ch1 := bus.subscribe()
	ch2 := bus.subscribe()
	defer bus.unsubscribe(ch1)
	defer bus.unsubscribe(ch2)

	if _, err := fmt.Fprintln(bus, "broadcast line"); err != nil {
		t.Fatalf("Write: %v", err)
	}

	for i, ch := range []chan string{ch1, ch2} {
		select {
		case got := <-ch:
			if got != "broadcast line" {
				t.Errorf("subscriber %d: got %q, want broadcast line", i, got)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("subscriber %d: timeout waiting for broadcast", i)
		}
	}
}

func TestLogBus_UnsubscribeStopsDelivery(t *testing.T) {
	bus := newLogBus()
	ch := bus.subscribe()

	bus.unsubscribe(ch)

	// Writing after unsubscribe must not block.
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := fmt.Fprintln(bus, "should not arrive"); err != nil {
			t.Errorf("Write after unsubscribe: %v", err)
		}
	}()

	select {
	case <-done:
		// success
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Write blocked after unsubscribe")
	}
}

func TestLogBus_DropsSlowClient(t *testing.T) {
	bus := newLogBus()
	ch := bus.subscribe() // channel capacity = 128
	defer bus.unsubscribe(ch)

	// Fill the channel well past capacity — Write must not block.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			_, _ = fmt.Fprintf(bus, "line %d\n", i)
		}
	}()

	select {
	case <-done:
		// success — all writes completed without blocking
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Write blocked for slow client")
	}
}

// ── SSE stream ────────────────────────────────────────────────────────────────

func TestStreamLogs_SendsConnectedComment(t *testing.T) {
	bus := newLogBus()
	srv := &Server{logBus: bus}

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/logs/stream", nil)

	// Cancel the request after a short time to stop the SSE handler.
	ctx, cancel := withTimeout(50 * time.Millisecond)
	defer cancel()

	srv.streamLogs(rec, r.WithContext(ctx))

	body := rec.Body.String()
	if !strings.Contains(body, ": connected") {
		t.Errorf("SSE response missing connected comment: %q", body)
	}
}

func TestStreamLogs_DeliversBroadcastLine(t *testing.T) {
	bus := newLogBus()
	srv := &Server{logBus: bus}

	// Broadcast a line into the bus just before the handler picks it up.
	go func() {
		time.Sleep(10 * time.Millisecond)
		_, _ = fmt.Fprintln(bus, "test log line")
	}()

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/logs/stream", nil)
	ctx, cancel := withTimeout(100 * time.Millisecond)
	defer cancel()

	srv.streamLogs(rec, r.WithContext(ctx))

	body := rec.Body.String()
	if !strings.Contains(body, "test log line") {
		t.Errorf("SSE stream missing test log line; body: %q", body)
	}
}

func TestStreamLogs_FormatIsSSE(t *testing.T) {
	bus := newLogBus()
	srv := &Server{logBus: bus}

	go func() {
		time.Sleep(10 * time.Millisecond)
		_, _ = fmt.Fprintln(bus, "hello from log")
	}()

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/logs/stream", nil)
	ctx, cancel := withTimeout(100 * time.Millisecond)
	defer cancel()

	srv.streamLogs(rec, r.WithContext(ctx))

	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type: got %q, want text/event-stream", ct)
	}

	// Each SSE event must be in "data: <payload>\n\n" format.
	scanner := bufio.NewScanner(strings.NewReader(rec.Body.String()))
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" && !strings.HasPrefix(line, ":") && !strings.HasPrefix(line, "data:") {
			t.Errorf("unexpected SSE line format: %q", line)
		}
	}
}

// ── helper ────────────────────────────────────────────────────────────────────

// withTimeout returns a context that expires after d, mimicking a client
// disconnecting after a short time.
func withTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
