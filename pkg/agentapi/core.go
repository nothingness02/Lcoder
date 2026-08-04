package agentapi

import (
	"context"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/task"
)

// CoreAPI is the stable protocol surface a UI (or any out-of-process client)
// uses to drive the agent. It supersedes agent.Runner for interactive use:
// the map-based Stats and the internal *task.Manager leak are replaced by
// structured DTOs, and session/mode/checkpoint write paths are semantic
// operations implemented by the host layer rather than the UI.
//
// Implementations: pkg/agent's *Agent provides the run-control, goal, task,
// context, and checkpoint pieces; pkg/host.Core composes an agent with the
// session/checkpoint stores to provide SetMode/OpenSession/NewSession/
// TruncateAfter. A CoreAPI handle is stable across mode switches — the host
// swaps the underlying runner internally.
//
// Single-session note: the protocol deliberately carries no sessionID/agentID
// routing fields (one session per process); that is the documented extension
// point for a future multi-session transport.
type CoreAPI interface {
	// Prompt starts a run with a user message.
	Prompt(ctx context.Context, msg models.AgentMessage) error
	// Continue starts a run without a new user message.
	Continue(ctx context.Context) error
	// Steer injects a user message during the next safe boundary.
	Steer(msg models.AgentMessage)
	// Abort signals the current run to stop gracefully.
	Abort()

	// AllMessages returns the full conversation (read-only query).
	AllMessages() []models.AgentMessage

	// SetUserConfirm wires the interactive permission approval callback.
	SetUserConfirm(uc UserConfirmation)

	// ContextStats returns the structured context token accounting.
	ContextStats() ContextStats
	// SetSkillsBlock writes (or, for an empty string, removes) the skills
	// context block.
	SetSkillsBlock(content string)

	// Mode returns the current permission mode.
	Mode() string
	// SetMode switches the permission mode. Implemented by the host, which
	// swaps the underlying runner; the CoreAPI handle stays valid.
	SetMode(mode string) error
	// SwitchModel changes the model for subsequent turns, preserving history.
	SwitchModel(ref models.ModelRef, budget contextmgr.TokenBudget)
	// SwitchThinking replaces the resolved thinking value for subsequent
	// turns ("" = send nothing, "off", "on", or a model effort level).
	SwitchThinking(thinking string)
	// Thinking returns the current resolved thinking value.
	Thinking() string

	// SessionID returns the session the agent is currently associated with.
	SessionID() string
	// OpenSession loads an existing session: messages, tasks, and session id
	// are swapped atomically by the host.
	OpenSession(sessionID string) error
	// NewSession starts a fresh, empty session.
	NewSession() error
	// TruncateAfter drops everything after the given message id (/retry).
	TruncateAfter(messageID string) error
	// ListSessions returns the metadata of the current project's sessions
	// (subagent journals included, flagged via SessionInfo.Subagent) for the
	// session picker. Implemented by the host, which owns the session store.
	ListSessions() ([]SessionInfo, error)
	// RenameSession assigns an explicit title to a session (/rename, picker
	// inline rename).
	RenameSession(sessionID, title string) error

	// Tasks returns a snapshot of the agent's declared task list. Runtime
	// updates arrive via events.TaskListUpdatedEvent.
	Tasks() []task.Task
	// ClearSkillFilter lifts any active skill tool restriction.
	ClearSkillFilter()

	// Goal returns a copy of the current goal record, or nil. It is the
	// initial query; subsequent updates arrive via events.GoalUpdatedEvent.
	Goal() *GoalState
	// StartGoal creates an active goal with the given budgets (0 = unlimited).
	StartGoal(objective string, turnBudget, tokenBudget int)
	// PauseGoal suspends an active goal; the driver exits at the boundary.
	PauseGoal(reason string)
	// ResumeGoal reactivates a paused/blocked goal.
	ResumeGoal()
	// CancelGoal clears the goal record.
	CancelGoal()
	// LastEndReason returns how the most recent run ended.
	LastEndReason() events.AgentEndReason
	// MicroCompactStatus returns the mechanical tool-result trimming status
	// ("" when disabled) for /status echo.
	MicroCompactStatus() string

	// SaveCheckpoint captures and persists the current state, returning the
	// new checkpoint's identifier.
	SaveCheckpoint() (string, error)
	// RestoreCheckpoint loads the checkpoint stored under id and applies it.
	RestoreCheckpoint(id string) error
	// ListCheckpoints lists the stored checkpoint entries.
	ListCheckpoints() ([]CheckpointInfo, error)
}
