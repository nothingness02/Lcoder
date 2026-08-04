package tui

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/agentapi"
	"github.com/lcoder/lcoder/pkg/host"
)

// runInputHook is applied to the model created by Run. Install it via
// SetInputHook during startup, before the program loop begins.
var runInputHook func(text string) (string, bool, string)

// SetInputHook installs the process-wide extension input hook applied to the
// model created by Run. A nil hook disables interception.
func SetInputHook(hook func(text string) (string, bool, string)) {
	runInputHook = hook
}

// Run starts the TUI application around the protocol handle and the local
// workbench services. The interactive approval callback is created here (it
// needs the bubbletea program) and wired into the core; wireConfirm, when
// non-nil, receives the same callback so the caller can wire additional
// consumers (the subagent host) that must survive mode switches.
func Run(core agentapi.CoreAPI, services host.Services, display DisplayConfig, wireConfirm func(agentapi.UserConfirmation)) error {
	model := NewModel(core, services, display)
	model.SetInputHook(runInputHook)
	defer model.Close()

	// Detect terminal background ONCE before bubbletea grabs stdin (the OSC 11
	// reply is swallowed otherwise and detection falls back to dark).
	warmBackgroundColor()

	program := tea.NewProgram(
		model,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	confirm := &tuiConfirm{program: program}
	core.SetUserConfirm(confirm)
	if wireConfirm != nil {
		wireConfirm(confirm)
	}
	if _, err := program.Run(); err != nil {
		return fmt.Errorf("run tui: %w", err)
	}
	// The alternate screen is gone now: print the transcript so the session
	// lives on in the terminal's native scrollback (opt-out via
	// tui.exit_transcript: false).
	if services.Config.TUI.ExitTranscript {
		writeTranscript(os.Stdout, model.blocks)
	}
	return nil
}

// RunWithIO starts the TUI with custom input/output for testing.
func RunWithIO(core agentapi.CoreAPI, services host.Services, display DisplayConfig, input *os.File, output *os.File) (tea.Model, error) {
	model := NewModel(core, services, display)
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
