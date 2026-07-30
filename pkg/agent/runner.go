package agent

import (
	"context"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/task"
)

// Runner abstracts the agent for UI interaction. It is defined in the agent
// package so the concrete Agent can implement it without a return-type
// indirection, and so mode switching can be expressed through an interface.
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
}

// ModeSwitcher extends Runner with the ability to create a new agent instance
// configured for a different mode. The new runner shares the same underlying
// services but snapshots the context manager so the mode's system prompt is
// applied on the next turn.
type ModeSwitcher interface {
	Runner
	WithMode(mode string) Runner
}
