package agent

import (
	"sync"

	"github.com/lcoder/lcoder/pkg/models"
)

// RuntimeState is a serializable snapshot of the agent runtime state that must
// survive across checkpoint save/restore boundaries.
type RuntimeState struct {
	State          State
	Turn           int
	SteeringQueue  []models.AgentMessage
	ActiveDeferred map[string]bool
}

// snapshot captures the current runtime state in a copy.
func (s *stateHolder) snapshot() RuntimeState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return RuntimeState{
		State:          s.state,
		Turn:           s.turn,
		SteeringQueue:  append([]models.AgentMessage(nil), s.steeringQueue...),
		ActiveDeferred: nil,
	}
}

// restore replaces the runtime state from a snapshot and prepares a fresh abort
// channel so a restored run can be aborted independently of the source holder.
func (s *stateHolder) restore(rs RuntimeState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = rs.State
	s.turn = rs.Turn
	s.steeringQueue = append([]models.AgentMessage(nil), rs.SteeringQueue...)
	s.abortCh = make(chan struct{})
	s.abortOnce = sync.Once{}
	s.streamAbort = nil
}

// snapshot captures the currently promoted deferred tools.
func (e *executor) snapshot() RuntimeState {
	e.mu.Lock()
	defer e.mu.Unlock()
	active := make(map[string]bool, len(e.activeDeferred))
	for k, v := range e.activeDeferred {
		active[k] = v
	}
	return RuntimeState{
		ActiveDeferred: active,
	}
}

// restore replaces the promoted deferred tools from a snapshot.
func (e *executor) restore(rs RuntimeState) {
	e.mu.Lock()
	defer e.mu.Unlock()
	active := make(map[string]bool, len(rs.ActiveDeferred))
	for k, v := range rs.ActiveDeferred {
		active[k] = v
	}
	e.activeDeferred = active
}
