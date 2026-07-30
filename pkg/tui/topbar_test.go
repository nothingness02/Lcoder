package tui

import (
	"strings"
	"testing"
)

func TestRenderTopBar(t *testing.T) {
	cases := []struct {
		name     string
		bar      topBarInfo
		width    int
		want     []string // substrings that must appear
		wantNot  []string
		emptyOut bool
	}{
		{
			name:  "full bar",
			bar:   topBarInfo{mode: "plan", model: "claude-sonnet-4-5", session: "a1b2c3d4e5f6", turn: 12},
			width: 80,
			want:  []string{"Lcoder", "plan", "claude-sonnet-4-5", "a1b2c3d4", "turn 12"},
		},
		{
			name:  "session truncated to 8 chars",
			bar:   topBarInfo{mode: "build", model: "m", session: "a1b2c3d4e5f6", turn: 1},
			width: 80,
			want:  []string{"a1b2c3d4"},
			wantNot: []string{"a1b2c3d4e5"},
		},
		{
			name:  "narrow terminal drops tail segments first",
			bar:   topBarInfo{mode: "plan", model: "claude-sonnet-4-5", session: "a1b2c3d4e5f6", turn: 12},
			width: 52,
			want:  []string{"plan"},
			wantNot: []string{"turn 12"},
		},
		{
			name:     "hidden below 50 cols",
			bar:      topBarInfo{mode: "plan", model: "m", session: "s", turn: 1},
			width:    49,
			emptyOut: true,
		},
		{
			name:  "empty mode shows ready",
			bar:   topBarInfo{mode: "", model: "m", session: "s12345678", turn: 0},
			width: 80,
			want:  []string{"ready"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := renderTopBar(c.bar, c.width)
			if c.emptyOut {
				if out != "" {
					t.Fatalf("expected empty output, got %q", out)
				}
				return
			}
			for _, w := range c.want {
				if !strings.Contains(out, w) {
					t.Errorf("renderTopBar missing %q in %q", w, out)
				}
			}
			for _, w := range c.wantNot {
				if strings.Contains(out, w) {
					t.Errorf("renderTopBar should not contain %q in %q", w, out)
				}
			}
		})
	}
}

func TestTopBarLayoutIntegration(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	m.width = 80
	m.height = 24
	m.topBar = false
	m.updateSizes()

	// The viewport must yield one row to the top bar.
	vhWithoutBar := m.viewport.Height
	m.topBar = true
	m.updateSizes()
	if m.viewport.Height != vhWithoutBar-1 {
		t.Fatalf("viewport height = %d, want %d (one row for the top bar)", m.viewport.Height, vhWithoutBar-1)
	}

	view := m.View()
	lines := strings.Split(view, "\n")
	if len(lines) > m.height+2 { // border slack
		t.Fatalf("frame taller than terminal: %d lines for height %d", len(lines), m.height)
	}
}
