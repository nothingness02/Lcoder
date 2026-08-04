package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/lcoder/lcoder/pkg/checkpoint"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/task"
)

// Checkpoint captures the agent's current mode, model, context manager state,
// and runtime state into a portable snapshot.
func (a *Agent) Checkpoint() (*checkpoint.Checkpoint, error) {
	if a.cpMgr != nil {
		return a.cpMgr.Capture(checkpoint.ReasonManual)
	}
	return a.captureWithReason(checkpoint.ReasonManual)
}

// CheckpointWithReason captures the agent state and records why the checkpoint
// was taken (e.g. manual slash command or automatic turn boundary).
func (a *Agent) CheckpointWithReason(reason string) (*checkpoint.Checkpoint, error) {
	if a.cpMgr != nil {
		return a.cpMgr.Capture(reason)
	}
	return a.captureWithReason(reason)
}

// captureWithReason contains the actual checkpoint serialization logic.
func (a *Agent) captureWithReason(reason string) (*checkpoint.Checkpoint, error) {
	mgrState, err := a.mgr.SnapshotRuntime()
	if err != nil {
		return nil, err
	}

	stateSnap := a.loopState.snapshot()
	execSnap := a.executor.snapshot()
	tmState := a.taskMgr.Snapshot()

	cp := &checkpoint.Checkpoint{
		Version: 0, // MarshalJSON will set CurrentVersion.
		Session: &checkpoint.SessionSnapshot{
			SessionID:    a.cfg.SessionID,
			CheckpointID: uuid.NewString(),
			Reason:       reason,
			ConfigHash:   a.agentConfigHash(),
		},
		Agent: &checkpoint.AgentSnapshot{
			Mode:           a.cfg.Mode,
			Model:          a.cfg.Model,
			MaxTurnsPerRun: a.cfg.MaxTurnsPerRun,
			DeferredTools:  a.cfg.DeferredTools,
			CoreTools:      append([]string(nil), a.cfg.CoreTools...),
			Goal:           goalSnapshotOf(a.goals.get()),
			Reminders:      a.injector.Snapshot(),
		},
		Context: &checkpoint.ContextSnapshot{
			Budget:             mgrState.Budget,
			EphemeralReminders: mgrState.EphemeralReminders,
			CachePolicy:        mgrState.CachePolicy,
			LastUsage:          mgrState.LastUsage,
			MinRecent:          mgrState.MinRecent,
			Blocks:             make([]checkpoint.BlockSnapshot, 0, len(mgrState.Blocks)),
		},
		Runtime: &checkpoint.RuntimeSnapshot{
			State:            int(stateSnap.State),
			Turn:             stateSnap.Turn,
			IsAtTurnBoundary: stateSnap.State == StateIdle,
			SteeringQueue:    stateSnap.SteeringQueue,
			ActiveDeferred:   execSnap.ActiveDeferred,
			TaskManagerState: &tmState,
		},
	}

	for _, b := range mgrState.Blocks {
		cp.Context.Blocks = append(cp.Context.Blocks, checkpoint.BlockSnapshot{
			Kind:             string(b.Kind),
			Name:             b.Name,
			Priority:         b.Priority,
			Stability:        string(b.Stability),
			Metadata:         b.Metadata,
			CacheHint:        string(b.CacheHint),
			LastModifiedTurn: b.LastModifiedTurn,
		})
	}

	return cp, nil
}

// agentConfigHash returns a stable hash of the runtime-relevant configuration
// so that Restore can detect environment drift.
// goalSnapshotOf converts the live goal record to its checkpoint form.
func goalSnapshotOf(g *GoalState) *checkpoint.GoalSnapshot {
	if g == nil {
		return nil
	}
	return &checkpoint.GoalSnapshot{
		Objective:   g.Objective,
		Status:      string(g.Status),
		TurnBudget:  g.TurnBudget,
		TokenBudget: g.TokenBudget,
		TurnsUsed:   g.TurnsUsed,
		TokensUsed:  g.TokensUsed,
		BlockReason: g.BlockReason,
	}
}

func (a *Agent) agentConfigHash() string {
	snap := checkpoint.AgentSnapshot{
		Mode:           a.cfg.Mode,
		Model:          a.cfg.Model,
		MaxTurnsPerRun: a.cfg.MaxTurnsPerRun,
		DeferredTools:  a.cfg.DeferredTools,
		CoreTools:      a.cfg.CoreTools,
	}
	data, _ := json.Marshal(snap)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Restore replaces the agent's mode, model, context manager state, and runtime
// state from a checkpoint.
//
// This is not a thread-safe hot-restore: it must be called at a safe boundary
// (e.g., while the agent is idle and waiting for user input) when no run loop,
// streamer, or executor is active.
func (a *Agent) Restore(cp *checkpoint.Checkpoint) error {
	if a.cpMgr != nil {
		return a.cpMgr.Restore(cp)
	}
	return a.restore(cp)
}

// restore contains the actual checkpoint restoration logic.
func (a *Agent) restore(cp *checkpoint.Checkpoint) error {
	if cp == nil {
		return fmt.Errorf("agent: cannot restore: nil checkpoint")
	}
	if cp.Agent == nil {
		return fmt.Errorf("agent: cannot restore: missing agent snapshot")
	}
	if cp.Session != nil {
		if a.cfg.SessionID != "" && cp.Session.SessionID != "" && cp.Session.SessionID != a.cfg.SessionID {
			return fmt.Errorf("agent: cannot restore: checkpoint session %q does not match agent session %q", cp.Session.SessionID, a.cfg.SessionID)
		}
		// ConfigHash is intentionally advisory: mode/model/tool flags are restored
		// from the checkpoint, so a hash mismatch here does not imply corruption.
	}

	a.cfg.Mode = cp.Agent.Mode
	a.cfg.Model = cp.Agent.Model
	a.cfg.MaxTurnsPerRun = cp.Agent.MaxTurnsPerRun
	a.cfg.DeferredTools = cp.Agent.DeferredTools
	a.cfg.CoreTools = append([]string(nil), cp.Agent.CoreTools...)

	// Reinstate the injector dedup bookkeeping so a resumed session keeps its
	// quiet cadence instead of re-injecting everything from scratch. Old
	// checkpoints omit the field and simply start with a clean cadence.
	if len(cp.Agent.Reminders) > 0 {
		a.injector.Restore(cp.Agent.Reminders)
	}

	mgrState := &contextmgr.ManagerState{
		Budget:             cp.Context.Budget,
		EphemeralReminders: cp.Context.EphemeralReminders,
		CachePolicy:        cp.Context.CachePolicy,
		LastUsage:          cp.Context.LastUsage,
		MinRecent:          cp.Context.MinRecent,
		Blocks:             make([]contextmgr.BlockState, 0, len(cp.Context.Blocks)),
	}

	for _, b := range cp.Context.Blocks {
		mgrState.Blocks = append(mgrState.Blocks, contextmgr.BlockState{
			Kind:             contextmgr.BlockKind(b.Kind),
			Name:             b.Name,
			Priority:         b.Priority,
			Stability:        contextmgr.Stability(b.Stability),
			Metadata:         b.Metadata,
			CacheHint:        contextmgr.CacheHint(b.CacheHint),
			LastModifiedTurn: b.LastModifiedTurn,
		})
	}

	if err := a.mgr.Restore(mgrState); err != nil {
		return err
	}

	a.loopState.restore(RuntimeState{
		State:         State(cp.Runtime.State),
		Turn:          cp.Runtime.Turn,
		SteeringQueue: cp.Runtime.SteeringQueue,
	})

	// Mark the holder so the next run continues from the restored turn counter
	// instead of resetting to 0.
	a.loopState.SetResuming(true)

	// A checkpointed active goal always degrades to paused: the process died
	// mid-pursuit, so resuming is the user's explicit decision (/goal resume).
	// This runs AFTER mgr.Restore/loopState.restore so the GoalUpdatedEvent it
	// emits carries the restored turn and subscribers observe a fully restored
	// context, not the pre-restore one.
	if cp.Agent.Goal != nil {
		g := &GoalState{
			Objective:   cp.Agent.Goal.Objective,
			Status:      GoalStatus(cp.Agent.Goal.Status),
			TurnBudget:  cp.Agent.Goal.TurnBudget,
			TokenBudget: cp.Agent.Goal.TokenBudget,
			TurnsUsed:   cp.Agent.Goal.TurnsUsed,
			TokensUsed:  cp.Agent.Goal.TokensUsed,
			BlockReason: cp.Agent.Goal.BlockReason,
		}
		if g.Status == GoalActive {
			g.Status = GoalPaused
			if g.BlockReason == "" {
				g.BlockReason = "restored after crash"
			}
		}
		a.goals.set(g)
	}

	a.executor.restore(RuntimeState{
		ActiveDeferred: cp.Runtime.ActiveDeferred,
	})

	if cp.Runtime.TaskManagerState != nil {
		if err := a.taskMgr.Restore(*cp.Runtime.TaskManagerState); err != nil {
			return fmt.Errorf("agent: restore task manager: %w", err)
		}
	} else {
		// Fallback: rebuild from message history for backwards compatibility.
		if tasks := latestTodos(a.mgr.AllMessages()); len(tasks) > 0 {
			_, _, _ = a.taskMgr.ReplaceAll(tasks)
		}
	}

	return nil
}

// latestTodos scans messages for the most recent todo_write tool call.
// It is kept as an internal fallback for restoring task state from old checkpoints
// that did not include TaskManagerState.
func latestTodos(messages []models.AgentMessage) []task.Task {
	for i := len(messages) - 1; i >= 0; i-- {
		for _, tc := range messages[i].ToolCalls() {
			if tc.Name == task.ToolName {
				if ts, err := task.Parse(tc.Arguments["todos"]); err == nil {
					return ts
				}
			}
		}
	}
	return nil
}
