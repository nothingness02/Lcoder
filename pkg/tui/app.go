package tui

import (
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/checkpoint"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/mcp"
	"github.com/lcoder/lcoder/pkg/session"
	"github.com/lcoder/lcoder/pkg/skills"
)

// runInputHook is applied to the model created by Run. Install it via
// SetInputHook during startup, before the program loop begins.
var runInputHook func(text string) (string, bool, string)

// SetInputHook installs the process-wide extension input hook applied to the
// model created by Run. A nil hook disables interception.
func SetInputHook(hook func(text string) (string, bool, string)) {
	runInputHook = hook
}

// Run starts the TUI application.
// onSessionChange, when non-nil, is notified whenever the active session is
// swapped (/sessions, /new) so the compaction sink records folds to the session
// actually in use.
func Run(bus *events.Bus, ag *agent.Agent, sess *session.Session, store *session.Store, cwd, modelRef, themeStyle string, httpTools []HTTPToolItem, mcpRegistry *mcp.Registry, modeManager *agent.ModeManager, capabilities []string, llmClient *llm.Client, cfg config.Config, needsProviderSetup bool, onSessionChange func(*session.Session), loadedSkillCatalog ...skills.SkillMeta) error {
	checkpointDir := filepath.Join(session.DefaultDir(), "checkpoints")
	checkpointStore := checkpoint.NewFileStore(checkpointDir)
	model := NewModel(bus, ag, sess, store, cwd, sess.ID, modelRef, themeStyle, httpTools, mcpRegistry, modeManager, llmClient, cfg, checkpointStore, needsProviderSetup, loadedSkillCatalog...)
	model.SetCapabilities(capabilities)
	model.SetInputHook(runInputHook)
	model.SetOnSessionChange(onSessionChange)
	defer model.Close()

	// Detect terminal background ONCE before bubbletea grabs stdin (the OSC 11
	// reply is swallowed otherwise and detection falls back to dark).
	warmBackgroundColor()

	program := tea.NewProgram(
		model,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	ag.SetUserConfirm(&tuiConfirm{program: program})
	if _, err := program.Run(); err != nil {
		return fmt.Errorf("run tui: %w", err)
	}
	return nil
}

// RunWithIO starts the TUI with custom input/output for testing.
func RunWithIO(bus *events.Bus, ag *agent.Agent, sess *session.Session, store *session.Store, cwd, modelRef, themeStyle string, httpTools []HTTPToolItem, mcpRegistry *mcp.Registry, modeManager *agent.ModeManager, llmClient *llm.Client, cfg config.Config, input *os.File, output *os.File, loadedSkillCatalog ...skills.SkillMeta) (tea.Model, error) {
	checkpointDir := filepath.Join(session.DefaultDir(), "checkpoints")
	checkpointStore := checkpoint.NewFileStore(checkpointDir)
	model := NewModel(bus, ag, sess, store, cwd, sess.ID, modelRef, themeStyle, httpTools, mcpRegistry, modeManager, llmClient, cfg, checkpointStore, false, loadedSkillCatalog...)
	defer model.Close()

	program := tea.NewProgram(
		model,
		tea.WithInput(input),
		tea.WithOutput(output),
	)
	m, err := program.Run()
	if err != nil {
		return nil, fmt.Errorf("run tui: %w", err)
	}
	return m, nil
}
