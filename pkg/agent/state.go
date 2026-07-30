package agent

import (
	"context"
	"sync"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
)

// stateHolder owns the agent runtime state, steering queue, and
// per-stream abort control. It exists so the top-level Agent can stay a
// coordinator rather than a God Object.
type stateHolder struct {
	mu            sync.Mutex
	state         State
	turn          int
	resuming      bool
	steeringQueue []models.AgentMessage

	// Loop-level abort.
	abortCh   chan struct{}
	abortOnce sync.Once

	// In-flight stream abort (set by the current turn's streamer).
	streamAbort context.CancelFunc

	// runCancel cancels the entire agent run context (stream + tools + compaction).
	runCancel context.CancelFunc

	// endReason is how the most recent run ended; read by loop-external
	// drivers (GoalDriver) after Prompt returns.
	endReason events.AgentEndReason
}

func newStateHolder() *stateHolder {
	return &stateHolder{abortCh: make(chan struct{})}
}

// Turn returns the current turn counter.
func (s *stateHolder) Turn() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turn
}

// SetTurn sets the turn counter.
func (s *stateHolder) SetTurn(t int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turn = t
}

// IncrTurn advances the turn counter by one.
func (s *stateHolder) IncrTurn() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turn++
}

// StartRun returns the turn at which a new run should begin. If the holder is
// marked as resuming (e.g., after checkpoint restore), it returns the saved turn
// and clears the flag; otherwise it resets to 0.
func (s *stateHolder) StartRun() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resuming {
		s.resuming = false
		return s.turn
	}
	s.turn = 0
	return 0
}

// SetResuming marks the holder so that the next run continues from the saved
// turn counter instead of resetting to 0.
func (s *stateHolder) SetResuming(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resuming = v
}

// State returns the current agent state.
func (s *stateHolder) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// SetState updates the agent state.
// SetEndReason records how the current run ended.
func (s *stateHolder) SetEndReason(r events.AgentEndReason) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.endReason = r
}

// LastEndReason returns how the most recent run ended.
func (s *stateHolder) LastEndReason() events.AgentEndReason {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.endReason
}

func (s *stateHolder) SetState(st State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = st
}

// ResetAbort prepares a fresh abort channel for a new run.
func (s *stateHolder) ResetAbort() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.abortCh = make(chan struct{})
	s.abortOnce = sync.Once{}
}

// Steer injects a user message during the next safe boundary.
func (s *stateHolder) Steer(msg models.AgentMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steeringQueue = append(s.steeringQueue, msg)
}

// Abort signals the current run to stop gracefully. Safe to call multiple times.
func (s *stateHolder) Abort() {
	s.CancelRun()

	s.mu.Lock()
	cancel := s.streamAbort
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}

	// Lock around the entire once+close sequence so ResetAbort cannot
	// replace abortOnce or abortCh between the Do check and the close.
	s.mu.Lock()
	s.abortOnce.Do(func() {
		close(s.abortCh)
	})
	s.mu.Unlock()
}

// SetStreamAbort registers the cancel function for the in-flight stream.
func (s *stateHolder) SetStreamAbort(cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streamAbort = cancel
}

// ClearStreamAbort unregisters the in-flight stream cancel function.
func (s *stateHolder) ClearStreamAbort() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streamAbort = nil
}

// SetRunCancel registers the cancel function for the whole run.
func (s *stateHolder) SetRunCancel(cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runCancel = cancel
}

// CancelRun cancels the active run context, if any.
func (s *stateHolder) CancelRun() {
	s.mu.Lock()
	cancel := s.runCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// DrainSteeringQueue returns and clears the steering queue.
func (s *stateHolder) DrainSteeringQueue() []models.AgentMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	msgs := s.steeringQueue
	s.steeringQueue = nil
	return msgs
}
