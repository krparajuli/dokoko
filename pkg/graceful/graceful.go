// Package graceful provides double-Ctrl-C confirmation and ordered service
// shutdown helpers shared across all dokoko binaries.
package graceful

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"dokoko.ai/dokoko/pkg/logger"
)

const confirmTimeout = 5 * time.Second

// ExitCh returns a channel that closes once the user confirms exit.
// The first SIGINT prints promptMsg and starts a confirmTimeout window;
// a second SIGINT within that window closes the channel.
// SIGTERM closes the channel immediately without confirmation.
// Intended for long-running processes that use select to also watch a
// context (e.g. server crash path).
func ExitCh(promptMsg string) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		sigCh := make(chan os.Signal, 3)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(sigCh)
		defer close(ch)

		for {
			sig := <-sigCh
			if sig == syscall.SIGTERM {
				return
			}
			// First SIGINT: ask for confirmation.
			fmt.Fprintln(os.Stderr, "\n[dokoko] "+promptMsg)
			t := time.NewTimer(confirmTimeout)
			select {
			case <-sigCh:
				t.Stop()
				return
			case <-t.C:
				fmt.Fprintln(os.Stderr, "[dokoko] Cancelled — still running.")
			}
		}
	}()
	return ch
}

// Service shuts down a named service, printing and logging before/after
// messages around the provided fn.  fn should perform the actual shutdown
// (it may block until complete).
func Service(name string, log *logger.Logger, fn func()) {
	msg := "Gracefully shutting down " + name
	fmt.Println(msg)
	log.Info(msg)
	fn()
	done := name + " shut down"
	fmt.Println(done)
	log.Info(done)
}

// PrintState prints and logs a single service state line.
func PrintState(name, status string, log *logger.Logger) {
	line := fmt.Sprintf("  %-26s %s", name, status)
	fmt.Println(line)
	log.Info(line)
}

// Done prints and logs the final "all services shut down" banner.
func Done(log *logger.Logger) {
	fmt.Println("\nAll services shut down.")
	fmt.Println("Now exiting.")
	log.Info("All services shut down. Now exiting.")
}
