package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/session"
)

// handleRenameCommand implements /rename: assign an explicit title to the
// current session. Without arguments it shows the current display title.
func handleRenameCommand(m *Model, args string) tea.Cmd {
	sess, ok := m.session.(*session.Session)
	if !ok {
		m.showTextPanel("rename", styleError().Render("no session to rename"))
		return nil
	}
	title := strings.TrimSpace(args)
	if title == "" {
		m.showTextPanel("rename", styleDim().Render("current title: "+sess.DisplayTitle()))
		return nil
	}
	if err := sess.SetTitle(title); err != nil {
		m.showTextPanel("rename", styleError().Render("rename failed: "+err.Error()))
		return nil
	}
	m.showTextPanel("rename", styleSuccess().Render("renamed to: "+title))
	return nil
}
