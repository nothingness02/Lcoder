package agent

import (
	"fmt"

	"github.com/lcoder/lcoder/pkg/agentapi"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/task"
)

// This file adapts *Agent to the agentapi.CoreAPI protocol surface. The
// run-control, goal, task, context, and checkpoint pieces are implemented
// here on *Agent itself; the session/mode write paths (SetMode, OpenSession,
// NewSession, TruncateAfter) are host-level operations and are NOT on *Agent.

// ContextStats returns the context manager's token accounting as a structured
// DTO, replacing the magic-key Stats() map for protocol consumers.
func (a *Agent) ContextStats() agentapi.ContextStats {
	out := agentapi.ContextStats{}
	if a.mgr == nil {
		return out
	}
	for k, v := range a.mgr.Stats() {
		switch k {
		case "total":
			out.Total = v
		case "budget_max":
			out.BudgetMax = v
		case "budget_target":
			out.BudgetTarget = v
		case "budget_output_reserve":
			out.BudgetOutputReserve = v
		case "drop_limit":
			out.DropLimit = v
		case "real_input":
			out.RealInput = v
		case "real_cache_read":
			out.RealCacheRead = v
		case "real_cache_creation":
			out.RealCacheCreation = v
		case "real_prompt_total":
			out.RealPromptTotal = v
		case "compaction_level":
			out.CompactionLevel = v
		default:
			// Per-block token estimates, keyed "kind:name".
			if out.Blocks == nil {
				out.Blocks = make(map[string]int)
			}
			out.Blocks[k] = v
		}
	}
	return out
}

// Tasks returns a snapshot of the agent's declared task list.
func (a *Agent) Tasks() []task.Task {
	if a.taskMgr == nil {
		return nil
	}
	return a.taskMgr.List()
}

// SetSkillsBlock writes (or, for an empty string, removes) the skills context
// block. It absorbs the updater closure the TUI used to install via
// ContextManager() (app.go skillsBlockUpdater).
func (a *Agent) SetSkillsBlock(content string) {
	if a.mgr == nil {
		return
	}
	if content == "" {
		a.mgr.RemoveBlock(contextmgr.BlockSkills, "skills")
		return
	}
	a.mgr.SetBlock(contextmgr.NewBlock(contextmgr.BlockSkills, "skills", contextmgr.StabilityStable, 90,
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: content})))
}

// SaveCheckpoint captures the current state with the manual reason, persists
// it under the agent's session id when a store is configured, and returns the
// identifier the checkpoint can be restored/listed under. With a store that
// is the store key (the session id, matching ListCheckpoints/RestoreCheckpoint);
// without one it falls back to the snapshot's own checkpoint id.
func (a *Agent) SaveCheckpoint() (string, error) {
	cp, err := a.Checkpoint()
	if err != nil {
		return "", err
	}
	if store := a.cfg.CheckpointStore; store != nil && a.cfg.SessionID != "" {
		if err := store.Save(a.cfg.SessionID, cp); err != nil {
			return "", fmt.Errorf("checkpoint save: %w", err)
		}
		return a.cfg.SessionID, nil
	}
	if cp.Session == nil {
		return "", nil
	}
	return cp.Session.CheckpointID, nil
}

// RestoreCheckpoint loads the checkpoint stored under id from the configured
// store and applies it. The same safe-boundary rules as Restore apply.
func (a *Agent) RestoreCheckpoint(id string) error {
	store := a.cfg.CheckpointStore
	if store == nil {
		return fmt.Errorf("agent: no checkpoint store configured")
	}
	cp, err := store.Load(id)
	if err != nil {
		return err
	}
	return a.Restore(cp)
}

// ListCheckpoints lists the identifiers of all stored checkpoints.
func (a *Agent) ListCheckpoints() ([]agentapi.CheckpointInfo, error) {
	store := a.cfg.CheckpointStore
	if store == nil {
		return nil, nil
	}
	ids, err := store.List()
	if err != nil {
		return nil, err
	}
	infos := make([]agentapi.CheckpointInfo, 0, len(ids))
	for _, id := range ids {
		infos = append(infos, agentapi.CheckpointInfo{ID: id})
	}
	return infos, nil
}
