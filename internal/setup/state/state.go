// Package setupstate tracks the one-shot startup setup lifecycle.
package setupstate

import "sync"

// Phase is the lifecycle stage of a setup run.
type Phase int

const (
	PhaseIdle    Phase = iota // not yet started
	PhaseRunning              // tasks in progress
	PhaseDone                 // all tasks succeeded
	PhaseFailed               // at least one task failed
)

func (p Phase) String() string {
	switch p {
	case PhaseIdle:
		return "idle"
	case PhaseRunning:
		return "running"
	case PhaseDone:
		return "done"
	case PhaseFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// State tracks the setup lifecycle.  All methods are safe for concurrent use.
type State struct {
	mu    sync.RWMutex
	phase Phase
	err   error
	done  chan struct{}
	once  sync.Once
}

// New returns a new State in PhaseIdle.
func New() *State {
	return &State{done: make(chan struct{})}
}

// SetRunning transitions to PhaseRunning.
func (s *State) SetRunning() {
	s.mu.Lock()
	s.phase = PhaseRunning
	s.mu.Unlock()
}

// SetDone transitions to PhaseDone and unblocks Done().
func (s *State) SetDone() {
	s.mu.Lock()
	s.phase = PhaseDone
	s.mu.Unlock()
	s.once.Do(func() { close(s.done) })
}

// SetFailed transitions to PhaseFailed with err and unblocks Done().
func (s *State) SetFailed(err error) {
	s.mu.Lock()
	s.phase = PhaseFailed
	s.err = err
	s.mu.Unlock()
	s.once.Do(func() { close(s.done) })
}

// Done returns a channel closed when setup reaches PhaseDone or PhaseFailed.
func (s *State) Done() <-chan struct{} {
	return s.done
}

// Phase returns the current phase.
func (s *State) Phase() Phase {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.phase
}

// Err returns the error recorded by SetFailed, or nil.
func (s *State) Err() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.err
}
