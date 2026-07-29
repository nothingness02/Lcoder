package agent

import (
	"context"

	"github.com/lcoder/lcoder/pkg/contextmgr"
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
}

// ModeSwitcher extends Runner with the ability to create a new agent instance
// configured for a different mode. The new runner shares the same underlying
// services but snapshots the context manager so the mode's system prompt is
// applied on the next turn.
type ModeSwitcher interface {
	Runner
	WithMode(mode string) Runner
}
