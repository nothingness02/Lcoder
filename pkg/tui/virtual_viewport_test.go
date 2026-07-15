package tui

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/tui/components"
)

type staticComponent struct {
	id    string
	lines int
	text  string
}

func (s staticComponent) ID() string                            { return s.id }
func (s staticComponent) Kind() components.BlockKind             { return components.BlockUser }
func (s staticComponent) Height(width int, expanded bool) int    { return s.lines }
func (s staticComponent) Render(width int, expanded bool) string {
	return strings.Repeat(s.text+"\n", s.lines)
}

func TestVirtualViewportRendersOnlyVisible(t *testing.T) {
	comps := []components.BlockComponent{
		staticComponent{id: "a", lines: 5, text: "A"},
		staticComponent{id: "b", lines: 5, text: "B"},
		staticComponent{id: "c", lines: 5, text: "C"},
	}
	// viewport height 5, scrolled to line 5: visible lines are 5-9, so only b is rendered.
	content := buildVirtualContent(comps, 80, 5, 5, false)
	if strings.Contains(content, "A") {
		t.Fatal("off-screen component A should not be rendered")
	}
	if !strings.Contains(content, "B") {
		t.Fatal("visible component B should be rendered")
	}
	if strings.Contains(content, "C") {
		t.Fatal("off-screen component C should not be rendered")
	}
}
