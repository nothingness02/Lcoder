package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// handleRenameCommand implements /rename: assign an explicit title to the
// current session. Without arguments it shows the current display title.
func handleRenameCommand(m *Model, args string) tea.Cmd {
	id := m.agent.SessionID()
	if id == "" {
		m.showTextPanel("rename", styleError().Render("no session to rename"))
		return nil
	}
	title := strings.TrimSpace(args)
	if title == "" {
		current := id
		if sessions, err := m.agent.ListSessions(); err == nil {
			for _, s := range sessions {
				if s.ID == id {
					current = s.Title
					break
				}
			}
		}
		m.showTextPanel("rename", styleDim().Render("current title: "+current))
		return nil
	}
	if err := m.agent.RenameSession(id, title); err != nil {
		m.showTextPanel("rename", styleError().Render("rename failed: "+err.Error()))
		return nil
	}
	m.showTextPanel("rename", styleSuccess().Render("renamed to: "+title))
	return nil
}
