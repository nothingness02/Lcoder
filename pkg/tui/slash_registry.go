package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/task"
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
		{Name: "rename", Description: "Rename current session (default: latest user message)", Category: "Session",
			Handler: func(m *Model, args string) tea.Cmd {
				return handleRenameCommand(m, args)
			}},
		{Name: "new", Aliases: []string{"clear"}, Description: "New session / clear chat", Category: "Session",
			Handler: func(m *Model, _ string) tea.Cmd {
				if m.store == nil {
					m.showTextPanel("new", styleError().Render("no session store available"))
					return nil
				}
				sess, err := m.store.Create(m.cwd)
				if err != nil {
					m.showTextPanel("new", styleError().Render(fmt.Sprintf("create session: %v", err)))
					return nil
				}
				m.session = sess
				if m.onSessionChange != nil {
					m.onSessionChange(sess)
				}
				m.runner.SetSession(sess)
				m.agent.SetSessionID(sess.ID)
				m.agent.SetMessages(nil)
				if tm := m.agent.TaskManager(); tm != nil {
					_ = tm.Restore(task.ManagerState{})
				}
				m.blocks = nil
				m.components = nil
				m.tasks = nil
				m.history = newInputHistory()
				m.suggestion = ""
				m.errMsg = ""
				m.completedTurns = 0
				m.rebuildViewport()
				return nil
			}},
		{Name: "save", Description: "Save current agent checkpoint", Category: "Session",
			Handler: func(m *Model, _ string) tea.Cmd {
				m.saveCheckpoint()
				return nil
			}},
		{Name: "goal", Description: "Pursue an objective across turns", Category: "Agent",
			Handler: func(m *Model, args string) tea.Cmd {
				return handleGoalCommand(m, args)
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
		{Name: "thinking", Description: "Set LLM thinking effort (off/on/low/medium/high)", Category: "Agent",
			Handler: func(m *Model, args string) tea.Cmd {
				m.handleThinkingCommand(strings.TrimSpace(args))
				return nil
			}},
		{Name: "skill", Description: "Trigger a skill", Category: "Agent",
			Handler: func(m *Model, _ string) tea.Cmd {
				m.openSkillPanel()
				return nil
			}},
		{Name: "skills", Description: "Manage skills (enable/disable)", Category: "Agent",
			Handler: func(m *Model, _ string) tea.Cmd {
				m.openSkillManagePanel()
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
		{Name: "color", Aliases: []string{"theme"}, Description: "Switch accent color", Category: "Theme",
			Handler: func(m *Model, _ string) tea.Cmd {
				m.openColorPanel()
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

// RegisterExtensionCommand registers a slash command backed by an external
// extension. Names conflicting with built-ins, aliases, or previously
// registered extension commands are rejected. usage is accepted for parity
// with extension manifests but not displayed (commandEntry has no usage field).
// Not safe for concurrent use with the running TUI; call during startup before
// the program loop begins.
func RegisterExtensionCommand(name, description, usage string, invoke func(args string) string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return fmt.Errorf("slash command name must not be empty")
	}
	if invoke == nil {
		return fmt.Errorf("slash command %q has nil invoke", name)
	}
	for _, e := range commandRegistry {
		if e.Name == name {
			return fmt.Errorf("slash command %q already registered", name)
		}
		for _, alias := range e.Aliases {
			if alias == name {
				return fmt.Errorf("slash command %q conflicts with alias of %q", name, e.Name)
			}
		}
	}
	commandRegistry = append(commandRegistry, commandEntry{
		Name:        name,
		Description: description,
		Category:    "Extension",
		Handler: func(m *Model, args string) tea.Cmd {
			m.addSystem(invoke(args))
			return nil
		},
	})
	return nil
}
