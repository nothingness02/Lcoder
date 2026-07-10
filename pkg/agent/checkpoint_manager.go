package agent

import (
	"context"
	"fmt"

	"github.com/lcoder/lcoder/pkg/checkpoint"
	"github.com/lcoder/lcoder/pkg/events"
)

// checkpointManager owns automatic and manual checkpoint persistence for an
// agent. It keeps the persistence policy (session id, interval, store) separate
// from the serialization logic that lives on the agent itself.
type checkpointManager struct {
	store     checkpoint.Store
	sessionID string
	interval  int
	agent     *Agent
}

// newCheckpointManager creates a manager for the agent. It returns nil when no
// checkpoint store or session id is configured, which means checkpoints are
// capture-only and never persisted automatically.
func newCheckpointManager(agent *Agent) *checkpointManager {
	if agent == nil || agent.cfg.CheckpointStore == nil || agent.cfg.SessionID == "" {
		return nil
	}
	interval := agent.cfg.CheckpointInterval
	if interval < 0 {
		interval = 0
	}
	return &checkpointManager{
		store:     agent.cfg.CheckpointStore,
		sessionID: agent.cfg.SessionID,
		interval:  interval,
		agent:     agent,
	}
}

// Capture serializes the current agent state for the given reason. It delegates
// to the agent's internal capture logic and is the common path used by both
// public Checkpoint methods and automatic checkpoints.
func (cm *checkpointManager) Capture(reason string) (*checkpoint.Checkpoint, error) {
	return cm.agent.captureWithReason(reason)
}

// Restore replaces the agent state from a checkpoint. Public Restore methods
// delegate here so that the manager stays in control of the restore boundary.
func (cm *checkpointManager) Restore(cp *checkpoint.Checkpoint) error {
	return cm.agent.restore(cp)
}

// MaybeCheckpoint writes an automatic checkpoint at a completed turn boundary
// when a store and session id are configured. The interval controls how often
// checkpoints are written: 0 means every turn; values > 0 save every N turns.
// Errors are emitted through emit but never stop the run.
func (cm *checkpointManager) MaybeCheckpoint(ctx context.Context, turn int, reason string, emit func(context.Context, events.Event)) {
	if cm == nil || cm.store == nil || cm.sessionID == "" {
		return
	}
	if cm.interval > 0 && turn%cm.interval != 0 {
		return
	}

	cp, err := cm.Capture(reason)
	if err != nil {
		emit(ctx, events.ErrorEvent{
			Base:    events.Base{Type: events.Error, Turn: turn},
			Message: "checkpoint: " + err.Error(),
		})
		return
	}
	if err := cm.store.Save(cm.sessionID, cp); err != nil {
		emit(ctx, events.ErrorEvent{
			Base:    events.Base{Type: events.Error, Turn: turn},
			Message: "checkpoint save: " + err.Error(),
		})
	}
}

// ManualCheckpoint captures and persists the current state with the manual
// reason. It is used by UI slash commands that explicitly request a checkpoint.
func (cm *checkpointManager) ManualCheckpoint() (*checkpoint.Checkpoint, error) {
	cp, err := cm.Capture(checkpoint.ReasonManual)
	if err != nil {
		return nil, err
	}
	if cm.store != nil && cm.sessionID != "" {
		if err := cm.store.Save(cm.sessionID, cp); err != nil {
			return cp, fmt.Errorf("checkpoint save: %w", err)
		}
	}
	return cp, nil
}
