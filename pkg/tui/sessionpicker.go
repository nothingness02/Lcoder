package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/lcoder/lcoder/pkg/session"
)

// SessionItem is a list item for the session picker.
type SessionItem struct {
	session *session.Session
}

func (s SessionItem) FilterValue() string { return s.session.DisplayTitle() + " " + s.session.ID }

func (s SessionItem) Title() string { return s.session.DisplayTitle() }
func (s SessionItem) Description() string {
	return fmt.Sprintf("%d messages · %s · %s", len(s.session.Messages), s.session.ID, s.session.CWD)
}

// SessionStore abstracts session operations needed by the TUI.
type SessionStore interface {
	List(cwd string) ([]*session.Session, error)
	LoadByID(cwd, id string) (*session.Session, error)
	Create(cwd string) (*session.Session, error)
}

// SessionPickerModel is an overlay for selecting sessions.
type SessionPickerModel struct {
	list    list.Model
	visible bool
	cwd     string
	store   SessionStore
	mode    string // select
	sess    *session.Session

	// renaming 内联重命名状态:r 进入,Enter 写回,Esc 取消。
	renaming bool
	input    textinput.Model
}

// NewSessionPicker creates a session picker.
func NewSessionPicker(store SessionStore, cwd, mode string, sess *session.Session) SessionPickerModel {
	items := []list.Item{}
	if sessions, err := store.List(cwd); err == nil {
		for _, s := range sessions {
			if s.IsSubagentJournal() {
				continue
			}
			items = append(items, SessionItem{session: s})
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
		cwd:     cwd,
		store:   store,
		mode:    mode,
		sess:    sess,
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
			m.input.SetValue(item.session.DisplayTitle())
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
				_ = item.session.SetTitle(m.input.Value())
				m.list.SetItem(m.list.Index(), SessionItem{session: item.session})
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

// Selected returns the currently selected session, if any.
func (m SessionPickerModel) Selected() *session.Session {
	if !m.visible {
		return nil
	}
	item, ok := m.list.SelectedItem().(SessionItem)
	if !ok {
		return nil
	}
	sess, err := m.store.LoadByID(m.cwd, item.session.ID)
	if err != nil {
		return nil
	}
	return sess
}
