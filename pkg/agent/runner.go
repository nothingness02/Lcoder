package agent

import (
	"context"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/task"
)

// Runner abstracts the agent for in-process driving. It is defined in the agent
// package so the concrete Agent can implement it without a return-type
// indirection, and so mode switching can be expressed through an interface.
//
// Deprecated for UI use: interactive surfaces (TUI, future transports) should
// depend on agentapi.CoreAPI instead. Runner is retained for in-process
// drivers (GoalDriver, headless mode, tests) that legitimately need the
// internal surface (Stats map, *task.Manager).
type Runner interface {
	Prompt(ctx context.Context, msg models.AgentMessage) error
	Continue(ctx context.Context) error
	AllMessages() []models.AgentMessage
	SetMessages(msgs []models.AgentMessage)
	SetUserConfirm(uc UserConfirmation)
	Stats() map[string]int
	Mode() string
	SessionID() string
	SetSessionID(id string)
	Steer(msg models.AgentMessage)
	Abort()
	SwitchModel(ref models.ModelRef, budget contextmgr.TokenBudget)
	// SwitchThinking replaces the resolved thinking value for subsequent
	// turns ("" = send nothing, "off", "on", or a model effort level).
	// The value must already be validated by engine.ResolveThinking.
	SwitchThinking(thinking string)
	// Thinking returns the current resolved thinking value ("" = send nothing).
	Thinking() string
	TaskManager() *task.Manager
	// ClearSkillFilter lifts any active skill tool restriction.
	ClearSkillFilter()
	// Goal returns a copy of the current goal record, or nil.
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
}

// ModeSwitcher extends Runner with the ability to create a new agent instance
// configured for a different mode. The new runner shares the same underlying
// services but snapshots the context manager so the mode's system prompt is
// applied on the next turn.
type ModeSwitcher interface {
	Runner
	WithMode(mode string) Runner
}
