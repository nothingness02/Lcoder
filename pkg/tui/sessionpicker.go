package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/lcoder/lcoder/pkg/agentapi"
)

// SessionItem is a list item for the session picker.
type SessionItem struct {
	info agentapi.SessionInfo
}

func (s SessionItem) FilterValue() string { return s.info.Title + " " + s.info.ID }

func (s SessionItem) Title() string { return s.info.Title }
func (s SessionItem) Description() string {
	return fmt.Sprintf("%d messages · %s · %s", s.info.MessageCount, s.info.ID, s.info.CWD)
}

// SessionPickerModel is an overlay for selecting sessions. It works purely off
// the protocol handle: the list comes from CoreAPI.ListSessions and the inline
// rename goes through CoreAPI.RenameSession, so the TUI never touches the
// session store.
type SessionPickerModel struct {
	list    list.Model
	visible bool
	core    AgentCore

	// renaming 内联重命名状态:r 进入,Enter 写回,Esc 取消。
	renaming bool
	input    textinput.Model
}

// NewSessionPicker creates a session picker; the list comes from the core.
func NewSessionPicker(core AgentCore) SessionPickerModel {
	items := []list.Item{}
	if sessions, err := core.ListSessions(); err == nil {
		for _, s := range sessions {
			if s.Subagent {
				continue
			}
			items = append(items, SessionItem{info: s})
		}
	}

	delegate := list.NewDefaultDelegate()
	delegate.Styles.SelectedTitle = delegate.Styles.SelectedTitle.Foreground(colorSelect)
	delegate.Styles.SelectedDesc = delegate.Styles.SelectedDesc.Foreground(colorSelectDesc)

	l := list.New(items, delegate, 40, 12)
	l.Title = "Sessions"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.Styles.Title = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)

	return SessionPickerModel{
		list:    l,
		visible: true,
		core:    core,
	}
}

// Hide closes the picker.
func (m *SessionPickerModel) Hide() {
	m.visible = false
}

// SetWidth adjusts the list to the bottom-strip width.
func (m *SessionPickerModel) SetWidth(w int) {
	if w > 0 {
		m.list.SetWidth(w)
	}
}

// Visible returns whether the picker is shown.
func (m SessionPickerModel) Visible() bool {
	return m.visible
}

// Update handles messages.
func (m SessionPickerModel) Update(msg tea.Msg) (SessionPickerModel, tea.Cmd) {
	if m.renaming {
		return m.updateRename(msg)
	}
	if k, ok := msg.(tea.KeyMsg); ok && k.Type == tea.KeyRunes && len(k.Runes) == 1 && k.Runes[0] == 'r' {
		if item, ok := m.list.SelectedItem().(SessionItem); ok {
			m.renaming = true
			m.input = textinput.New()
			m.input.SetValue(item.info.Title)
			m.input.CursorEnd()
			m.input.Focus()
			m.input.Width = max(20, m.list.Width()-8)
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

// updateRename routes keys to the inline rename input.
func (m SessionPickerModel) updateRename(msg tea.Msg) (SessionPickerModel, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.Type {
		case tea.KeyEsc:
			m.renaming = false
			return m, nil
		case tea.KeyEnter:
			if item, ok := m.list.SelectedItem().(SessionItem); ok {
				title := m.input.Value()
				if err := m.core.RenameSession(item.info.ID, title); err == nil {
					item.info.Title = title
					m.list.SetItem(m.list.Index(), item)
				}
			}
			m.renaming = false
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// Renaming reports whether the inline rename input is active.
func (m SessionPickerModel) Renaming() bool { return m.renaming }

// RenameValue returns the current inline rename input value.
func (m SessionPickerModel) RenameValue() string { return m.input.Value() }

// View renders the picker.
func (m SessionPickerModel) View() string {
	if !m.visible {
		return ""
	}
	if m.renaming {
		return m.list.View() + "\n" + styleDim().Render("rename (enter: save · esc: cancel): ") + m.input.View()
	}
	return m.list.View()
}

// Selected returns the id of the currently selected session, if any.
func (m SessionPickerModel) Selected() string {
	if !m.visible {
		return ""
	}
	item, ok := m.list.SelectedItem().(SessionItem)
	if !ok {
		return ""
	}
	return item.info.ID
}
