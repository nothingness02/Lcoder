package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// SlashHandler executes a slash command. It receives the TUI model and any
// arguments typed after the command name. It may return a Bubble Tea Cmd.
type SlashHandler func(*Model, string) tea.Cmd

// commandEntry describes one slash command. Dispatch lives in keys.go and
// routes via Handler instead of a hard-coded switch.
type commandEntry struct {
	Name        string
	Aliases     []string
	Description string
	Category    string
	Handler     SlashHandler
}

// commandRegistry is populated in init() so that handler closures do not create
// an initialization cycle with functions that transitively reference menuMatches.
var commandRegistry []commandEntry

func init() {
	commandRegistry = []commandEntry{
		{Name: "help", Aliases: []string{"?"}, Description: "Show help", Category: "System",
			Handler: func(m *Model, _ string) tea.Cmd {
				m.showTextPanel("help", formatCommandHelp())
				return nil
			}},
		{Name: "sessions", Aliases: []string{"resume", "continue"}, Description: "Switch session", Category: "Session",
			Handler: func(m *Model, _ string) tea.Cmd {
				m.openSessionPicker()
				return nil
			}},
		{Name: "new", Aliases: []string{"clear"}, Description: "New session / clear chat", Category: "Session",
			Handler: func(m *Model, _ string) tea.Cmd {
				m.blocks = nil
				m.rebuildViewport()
				return nil
			}},
		{Name: "save", Description: "Save current agent checkpoint", Category: "Session",
			Handler: func(m *Model, _ string) tea.Cmd {
				m.saveCheckpoint()
				return nil
			}},
		{Name: "restore", Description: "Restore agent checkpoint", Category: "Session",
			Handler: func(m *Model, _ string) tea.Cmd {
				m.restoreCheckpoint()
				return nil
			}},
		{Name: "checkpoints", Description: "List saved checkpoints", Category: "Session",
			Handler: func(m *Model, _ string) tea.Cmd {
				m.listCheckpoints()
				return nil
			}},
		{Name: "mode", Description: "Switch agent mode", Category: "Agent",
			Handler: func(m *Model, args string) tea.Cmd {
				m.switchMode(strings.TrimSpace(args))
				return nil
			}},
		{Name: "modes", Description: "List available modes", Category: "Agent",
			Handler: func(m *Model, _ string) tea.Cmd {
				m.openModePanel()
				return nil
			}},
		{Name: "provider", Aliases: []string{"model"}, Description: "Configure LLM provider / model", Category: "Agent",
			Handler: func(m *Model, _ string) tea.Cmd {
				m.openProviderPanel()
				return nil
			}},
		{Name: "skill", Description: "Trigger a skill", Category: "Agent",
			Handler: func(m *Model, _ string) tea.Cmd {
				m.openSkillPanel()
				return nil
			}},
		{Name: "tools", Description: "Toggle detailed tool & thinking view (Ctrl+O)", Category: "View",
			Handler: func(m *Model, _ string) tea.Cmd {
				m.toolsExpanded = !m.toolsExpanded
				m.rebuildViewport()
				return nil
			}},
		{Name: "tasks", Description: "Toggle task sidebar (Ctrl+T)", Category: "View",
			Handler: func(m *Model, _ string) tea.Cmd {
				m.toggleTaskSidebar()
				return nil
			}},
		{Name: "extensions", Aliases: []string{"ext"}, Description: "Toggle extensions panel", Category: "View",
			Handler: func(m *Model, _ string) tea.Cmd {
				m.extPanel.Visible = true
				m.state = stateExtensions
				return nil
			}},
		{Name: "mcp", Description: "Manage MCP servers", Category: "View",
			Handler: func(m *Model, _ string) tea.Cmd {
				m.openMCPPanel()
				return nil
			}},
		{Name: "retry", Description: "Retry last turn", Category: "Action",
			Handler: func(m *Model, _ string) tea.Cmd {
				return m.retryLast()
			}},
		{Name: "status", Description: "View system status", Category: "System",
			Handler: func(m *Model, _ string) tea.Cmd {
				m.showTextPanel("status", m.statusText())
				return nil
			}},
		{Name: "quit", Aliases: []string{"q"}, Description: "Quit", Category: "System",
			Handler: func(_ *Model, _ string) tea.Cmd {
				return tea.Quit
			}},
	}
}

// RegisterCommand adds a command to the slash command registry. Extensions and
// initialization code can use this to expose new TUI commands without editing
// the central switch statement.
func RegisterCommand(e commandEntry) {
	commandRegistry = append(commandRegistry, e)
}
