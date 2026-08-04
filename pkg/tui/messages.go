package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/agentapi"
	"github.com/lcoder/lcoder/pkg/events"
)

// AgentCore is the UI-facing agent protocol handle. It is an alias to the
// protocol boundary's CoreAPI so the TUI depends on pkg/agentapi, never on
// the agent implementation (see pkg/agentapi for the import discipline).
type AgentCore = agentapi.CoreAPI

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
