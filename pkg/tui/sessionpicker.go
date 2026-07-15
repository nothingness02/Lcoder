package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/lcoder/lcoder/pkg/session"
)

// SessionItem is a list item for the session picker.
type SessionItem struct {
	session session.Session
}

func (s SessionItem) FilterValue() string { return s.session.ID }

func (s SessionItem) Title() string { return s.session.ID }
func (s SessionItem) Description() string {
	return fmt.Sprintf("%d messages · %s", len(s.session.Messages), s.session.CWD)
}

// SessionStore abstracts session operations needed by the TUI.
type SessionStore interface {
	List(cwd string) ([]session.Session, error)
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
}

// NewSessionPicker creates a session picker.
func NewSessionPicker(store SessionStore, cwd, mode string, sess *session.Session) SessionPickerModel {
	items := []list.Item{}
	if sessions, err := store.List(cwd); err == nil {
		for _, s := range sessions {
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

// Visible returns whether the picker is shown.
func (m SessionPickerModel) Visible() bool {
	return m.visible
}

// Update handles messages.
func (m SessionPickerModel) Update(msg tea.Msg) (SessionPickerModel, tea.Cmd) {
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

// View renders the picker.
func (m SessionPickerModel) View() string {
	if !m.visible {
		return ""
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
