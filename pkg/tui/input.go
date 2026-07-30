package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	inputMinHeight = 1
	inputMaxHeight = 6
)

// InputModel wraps bubbles/textarea for the composer.
type InputModel struct {
	textarea   textarea.Model
	focused    bool
	width      int
	processing bool // dim border while the agent runs
}

// NewInputModel creates an input model with placeholder and styling.
func NewInputModel() InputModel {
	ta := textarea.New()
	ta.Placeholder = "Type a message…"
	ta.Prompt = "› "
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetHeight(inputMinHeight)
	ta.SetWidth(80)
	ta.Focus()
	// Strip the textarea's own focused styling so our border owns the frame.
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	return InputModel{textarea: ta, focused: true, width: 80}
}

// SetSize updates the textarea size.
//
// Deprecated: kept for the legacy model.go layout; the rewrite uses SetWidth +
// SyncHeight. Removed in Phase 13.
func (m *InputModel) SetSize(width, height int) {
	m.textarea.SetWidth(width)
	m.textarea.SetHeight(height)
}

// SetWidth sets the inner textarea width (border adds 2).
func (m *InputModel) SetWidth(width int) {
	m.width = width
	m.textarea.SetWidth(width)
}

// desiredHeight returns the auto-grow height clamped to [min,max].
func (m InputModel) desiredHeight() int {
	lines := strings.Count(m.textarea.Value(), "\n") + 1
	if lines < inputMinHeight {
		lines = inputMinHeight
	}
	if lines > inputMaxHeight {
		lines = inputMaxHeight
	}
	return lines
}

// SyncHeight applies desiredHeight to the textarea.
func (m *InputModel) SyncHeight() { m.textarea.SetHeight(m.desiredHeight()) }

func (m *InputModel) SetProcessing(p bool) { m.processing = p }

// Focus gives the input focus.
func (m *InputModel) Focus() {
	m.textarea.Focus()
	m.focused = true
}

// Blur removes focus.
func (m *InputModel) Blur() {
	m.textarea.Blur()
	m.focused = false
}

// Value returns the current input text.
func (m *InputModel) Value() string {
	return m.textarea.Value()
}

// CursorOffset returns the cursor's absolute rune offset within Value().
// textarea exposes the cursor as (hard-line row, rune column); the offset is
// the sum of the preceding lines' rune lengths plus their newline separators.
func (m InputModel) CursorOffset() int {
	val := m.textarea.Value()
	row := m.textarea.Line()
	li := m.textarea.LineInfo()
	col := li.StartColumn + li.ColumnOffset
	off := 0
	for i, line := range strings.Split(val, "\n") {
		if i == row {
			return off + min(col, len([]rune(line)))
		}
		off += len([]rune(line)) + 1
	}
	return off
}

// Reset clears the input.
func (m *InputModel) Reset() {
	m.textarea.Reset()
	m.textarea.SetHeight(inputMinHeight)
	m.textarea.Focus()
}

// Update handles bubbletea updates.
func (m InputModel) Update(msg tea.Msg) (InputModel, tea.Cmd) {
	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	return m, cmd
}

// View renders the input area with a rounded border. The border uses the accent
// color while focused and idle, and the faint color while the agent is running.
func (m InputModel) View() string {
	border := colorFaint
	if m.focused && !m.processing {
		border = colorAccent
	}
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border)
	return style.Render(m.textarea.View())
}
