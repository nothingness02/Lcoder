package tui

import (
	"strings"
	"testing"
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
