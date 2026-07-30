package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestInputAutoGrow(t *testing.T) {
	m := NewInputModel()
	m.SetWidth(40)
	m.textarea.SetValue("one\ntwo\nthree")
	if h := m.desiredHeight(); h < 3 {
		t.Fatalf("desiredHeight = %d, want >= 3", h)
	}
}

func TestInputHeightCapped(t *testing.T) {
	m := NewInputModel()
	m.SetWidth(40)
	m.textarea.SetValue("a\nb\nc\nd\ne\nf\ng\nh\ni")
	if h := m.desiredHeight(); h > 6 {
		t.Fatalf("desiredHeight = %d, want <= 6", h)
	}
}

// SetWidth(40) leaves a 38-cell wrap width (2-cell prompt). Expected rows
// below are hand-computed against bubbles/textarea's greedy word-wrap.
func TestInputVisualHeight(t *testing.T) {
	words20 := strings.Repeat("aaaaa ", 19) + "aaaaa"
	cases := []struct {
		name string
		val  string
		want int
	}{
		{"empty", "", 1},
		{"short single line", "hello", 1},
		{"long unbroken ascii", strings.Repeat("a", 100), 3},
		{"cjk double width", strings.Repeat("改", 20), 2},
		{"exact fit wraps", strings.Repeat("a", 38), 2},
		{"one under exact fit", strings.Repeat("a", 37), 1},
		{"word wrap", words20, 4},
		{"mixed hard and soft", strings.Repeat("a", 40) + "\nbb", 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := NewInputModel()
			m.SetWidth(40)
			m.textarea.SetValue(c.val)
			if h := m.desiredHeight(); h != c.want {
				t.Fatalf("desiredHeight = %d, want %d (value %q)", h, c.want, c.val)
			}
		})
	}
}

func TestInputValue(t *testing.T) {
	m := NewInputModel()
	m.textarea.SetValue("hi")
	if m.Value() != "hi" {
		t.Fatalf("Value = %q", m.Value())
	}
}

// Regression: typing past the wrap width grows the composer, but the
// textarea scrolled its internal viewport while it was still one row high —
// the grown view then shows only the tail and the head of the input is lost.
// The per-keystroke View() call mirrors the real TUI render loop; without it
// the textarea viewport has no content and the scroll never engages (which is
// why this only reproduces with View in the loop).
func TestInputGrowKeepsHeadVisible(t *testing.T) {
	m := NewInputModel()
	m.SetWidth(40) // 38-cell wrap width
	m.SyncHeight()
	for range 50 {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
		m.SyncHeight()
		_ = m.textarea.View() // real TUI renders every frame
	}
	view := m.textarea.View()
	if !strings.Contains(view, strings.Repeat("a", 38)) {
		t.Fatalf("first wrapped row hidden after auto-grow:\n%q", view)
	}
}

// Mid-text editing: the scroll reset must not teleport the cursor to the end
// of the input.
func TestInputGrowPreservesCursor(t *testing.T) {
	m := NewInputModel()
	m.SetWidth(40)
	m.textarea.SetValue(strings.Repeat("b", 50)) // 2 visual rows
	m.textarea.CursorStart()                     // row 0? cursor to line start
	before := m.CursorOffset()
	m.SyncHeight()
	if got := m.CursorOffset(); got != before {
		t.Fatalf("cursor moved by SyncHeight: before=%d after=%d", before, got)
	}
}

func TestCursorOffset(t *testing.T) {
	cases := []struct {
		name string
		val  string
		move func(m *InputModel)
		want int
	}{
		{"end of single line", "hello", nil, 5},
		{"start of single line", "hello", func(m *InputModel) { m.textarea.CursorStart() }, 0},
		{"end of second line", "ab\ncde", nil, 6},
		{"start of second line", "ab\ncde", func(m *InputModel) { m.textarea.CursorStart() }, 3},
		{"mid second line", "ab\ncde", func(m *InputModel) { m.textarea.SetCursor(1) }, 4},
		{"cjk end", "改一下", nil, 3},
		{"empty", "", nil, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := NewInputModel()
			m.SetWidth(80)
			m.textarea.SetValue(c.val)
			if c.move != nil {
				c.move(&m)
			}
			if got := m.CursorOffset(); got != c.want {
				t.Fatalf("CursorOffset() = %d, want %d", got, c.want)
			}
		})
	}
}
