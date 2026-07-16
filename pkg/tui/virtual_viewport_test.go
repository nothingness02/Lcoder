package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/lcoder/lcoder/pkg/tui/components"
)

type staticComponent struct {
	id    string
	lines int
	text  string
}

func (s staticComponent) ID() string                          { return s.id }
func (s staticComponent) Kind() components.BlockKind          { return components.BlockUser }
func (s staticComponent) Height(width int, expanded bool) int { return s.lines }
func (s staticComponent) Render(width int, expanded bool) string {
	return strings.Repeat(s.text+"\n", s.lines)
}

func TestVirtualViewportPreservesTotalHeight(t *testing.T) {
	comps := []components.BlockComponent{
		staticComponent{id: "a", lines: 3, text: "A"},
		staticComponent{id: "b", lines: 2, text: "B"},
		staticComponent{id: "c", lines: 4, text: "C"},
		staticComponent{id: "d", lines: 1, text: "D"},
	}
	totalHeight := 3 + 2 + 4 + 1

	lineCount := func(s string) int {
		if s == "" {
			return 0
		}
		return strings.Count(s, "\n") + 1
	}

	for scrollY := 0; scrollY <= totalHeight; scrollY++ {
		layouts := layoutComponents(comps, 80, false, -1)
		content := buildVirtualContent(layouts, 5, scrollY, false, -1)
		got := lineCount(content)
		if got != totalHeight {
			t.Fatalf("scrollY=%d: expected %d lines, got %d", scrollY, totalHeight, got)
		}
	}
}

func TestVirtualViewportRendersOnlyVisible(t *testing.T) {
	comps := []components.BlockComponent{
		staticComponent{id: "a", lines: 5, text: "A"},
		staticComponent{id: "b", lines: 5, text: "B"},
		staticComponent{id: "c", lines: 5, text: "C"},
	}
	// viewport height 5, scrolled to line 5: visible lines are 5-9, so only b is rendered.
	layouts := layoutComponents(comps, 80, false, -1)
	content := buildVirtualContent(layouts, 5, 5, false, -1)
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

func TestFocusedComponentRendersLeftBorder(t *testing.T) {
	comps := []components.BlockComponent{
		staticComponent{id: "a", lines: 3, text: "A"},
		staticComponent{id: "b", lines: 2, text: "B"},
	}
	layouts := layoutComponents(comps, 20, false, 0)
	content := buildVirtualContent(layouts, 10, 0, false, 0)
	if !strings.Contains(content, "┃") {
		t.Fatalf("expected left border in focused component output, got %q", content)
	}
	// Each rendered line should still fit within the original viewport width.
	for _, ln := range strings.Split(content, "\n") {
		if w := lipgloss.Width(ln); w > 20 {
			t.Fatalf("line width %d exceeds viewport width 20: %q", w, ln)
		}
	}
}

func TestFocusedComponentHeightMatchesLayout(t *testing.T) {
	comps := []components.BlockComponent{
		staticComponent{id: "a", lines: 3, text: "A"},
	}
	layouts := layoutComponents(comps, 20, false, 0)
	content := buildVirtualContent(layouts, 10, 0, false, 0)
	if got := strings.Count(content, "\n") + 1; got != 3 {
		t.Fatalf("expected 3 lines for focused component, got %d", got)
	}
}
