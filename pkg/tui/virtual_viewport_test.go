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
		content := buildVirtualContent(comps, 80, 5, scrollY, false)
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
