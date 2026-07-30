package tui

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
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

// desiredHeight returns the auto-grow height clamped to [min,max]. It counts
// soft-wrapped visual rows (textarea.Width() already excludes the prompt), so
// a long unbroken line grows the composer instead of hiding its head.
func (m InputModel) desiredHeight() int {
	lines := visualLineCount(m.textarea.Value(), m.textarea.Width())
	return clamp(lines, inputMinHeight, inputMaxHeight)
}

// visualLineCount returns the total soft-wrapped row count of text at the
// given cell width, mirroring bubbles/textarea's greedy word-wrap so the
// composer height tracks the textarea's actual layout.
func visualLineCount(text string, width int) int {
	if width <= 0 {
		return strings.Count(text, "\n") + 1
	}
	total := 0
	for _, hard := range strings.Split(text, "\n") {
		total += wrapRows(hard, width)
	}
	return total
}

// wrapRows counts the soft-wrapped rows of one hard line. It mirrors
// textarea.wrap: words flush at space boundaries, over-long words break by
// cells, and an exactly-full final row wraps once more (textarea reserves a
// trailing cell for the cursor).
func wrapRows(line string, width int) int {
	rows, rowW, wordW, spaces := 1, 0, 0, 0
	for _, r := range line {
		if unicode.IsSpace(r) {
			spaces++
		} else {
			wordW += runewidth.RuneWidth(r)
		}
		if spaces > 0 {
			if rowW+wordW+spaces > width {
				rows++
				rowW = wordW + spaces
			} else {
				rowW += wordW + spaces
			}
			wordW, spaces = 0, 0
		} else if wordW+runewidth.RuneWidth(r) > width {
			// The word alone fills the row; move it to a fresh one.
			if rowW > 0 {
				rows++
			}
			rowW = wordW
			wordW = 0
		}
	}
	if rowW+wordW+spaces >= width {
		rows++
	}
	return rows
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
