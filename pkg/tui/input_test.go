package tui

import "testing"

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
