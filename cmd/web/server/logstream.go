package server

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"sync"
)

// logBus is an io.Writer that broadcasts every written line to all active SSE clients.
type logBus struct {
	mu      sync.RWMutex
	clients map[chan string]struct{}
}

func newLogBus() *logBus {
	return &logBus{clients: make(map[chan string]struct{})}
}

// Write satisfies io.Writer; called by the logger on every emitted line.
func (b *logBus) Write(p []byte) (int, error) {
	line := string(bytes.TrimRight(p, "\n\r"))
	b.mu.RLock()
	for ch := range b.clients {
		select {
		case ch <- line:
		default: // slow client; drop
		}
	}
	b.mu.RUnlock()
	// Mirror to stderr so there is always a fallback when no SSE clients are connected.
	fmt.Fprintln(os.Stderr, string(bytes.TrimRight(p, "\n\r")))
	return len(p), nil
}

func (b *logBus) subscribe() chan string {
	ch := make(chan string, 128)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *logBus) unsubscribe(ch chan string) {
	b.mu.Lock()
	delete(b.clients, ch)
	b.mu.Unlock()
}

// streamLogs is an SSE handler that pushes log lines to the browser in real time.
func (s *Server) streamLogs(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := s.logBus.subscribe()
	defer s.logBus.unsubscribe(ch)

	// Send a heartbeat comment to establish the stream.
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case line := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", line)
			flusher.Flush()
		}
	}
}
