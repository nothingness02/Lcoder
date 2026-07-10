package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/checkpoint"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
)

// Checkpoint aliases so the TUI can type-assert the agent for snapshot operations.
type CheckpointSource = checkpoint.Source
type CheckpointTarget = checkpoint.Target

// EventMsg carries an agent event from the events bus into bubbletea.
type EventMsg struct {
	Event events.Event
}

// AgentDoneMsg signals that a Prompt run finished.
type AgentDoneMsg struct {
	Err error
}

// SendPromptMsg triggers a prompt submission.
type SendPromptMsg struct {
	Text string
}

// waitForEventCmd blocks until an event arrives on the channel.
func waitForEventCmd(ch <-chan events.Event) tea.Cmd {
	return func() tea.Msg {
		return EventMsg{Event: <-ch}
	}
}

// waitForRunnerResultCmd blocks until the runner queue produces a result.
func waitForRunnerResultCmd(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-ch
	}
}

// AgentRunner is the UI-facing agent interface. It is an alias to the agent
// package's Runner so the concrete *agent.Agent can satisfy ModeSwitcher without
// an extra indirection.
type AgentRunner = agent.Runner

// ModeSwitcher extends AgentRunner with mode switching capabilities.
type ModeSwitcher = agent.ModeSwitcher

// Compile-time assertion that the production *agent.Agent can be used through
// the TUI's ModeSwitcher alias.
var _ ModeSwitcher = (*agent.Agent)(nil)

// SessionWriter abstracts session persistence.
type SessionWriter interface {
	Append(msg models.AgentMessage) error
	SessionID() string
}
