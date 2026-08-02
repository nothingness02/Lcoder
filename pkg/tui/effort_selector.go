package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// effortSelector is a horizontal segmented picker for LLM thinking effort,
// mirroring kimi-code's EffortSelectorComponent: one row of segments, the
// active one wrapped in [ ]. ←/→ step, Enter persists, Alt+S applies to the
// current session only, Esc cancels.
type effortSelector struct {
	// efforts are the selectable values (e.g. ["off","low","medium","high"]).
	efforts []string
	// current is the currently active effort, highlighted on open.
	current string
	// warning is shown below the segments (e.g. the cache-invalidation
	// notice on a mid-conversation switch).
	warning string
	// activeIndex is the segment the cursor sits on.
	activeIndex int
}

// newEffortSelector builds a selector centered on current (or the first
// segment when current is not in efforts).
func newEffortSelector(efforts []string, current, warning string) *effortSelector {
	idx := 0
	for i, e := range efforts {
		if e == current {
			idx = i
			break
		}
	}
	return &effortSelector{efforts: efforts, current: current, warning: warning, activeIndex: idx}
}

// move steps the selection left/right, clamping within range.
func (s *effortSelector) move(delta int) {
	s.activeIndex = clamp(s.activeIndex+delta, 0, max(len(s.efforts)-1, 0))
}

// selected returns the effort at the cursor.
func (s *effortSelector) selected() string {
	if s.activeIndex < 0 || s.activeIndex >= len(s.efforts) {
		return ""
	}
	return s.efforts[s.activeIndex]
}

// render draws the panel in the same rounded box the slash menu uses.
func (s *effortSelector) render(width int) string {
	var lines []string
	lines = append(lines, styleDim().Render("select thinking effort"))
	hint := "←→ switch · Enter persist · Alt+S session-only · Esc cancel"
	lines = append(lines, styleFaint().Render(hint))
	if s.warning != "" {
		for _, ln := range strings.Split(s.warning, "\n") {
			lines = append(lines, styleWarn().Render("  "+ln))
		}
	}
	lines = append(lines, "")

	var segments []string
	for i, e := range s.efforts {
		if i == s.activeIndex {
			segments = append(segments, lipgloss.NewStyle().Foreground(colorSelect).Bold(true).Render("[ "+e+" ]"))
		} else {
			segments = append(segments, styleDim().Render("  "+e+"  "))
		}
	}
	lines = append(lines, strings.Join(segments, "  "))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorFaint)
	return box.Render(strings.Join(lines, "\n"))
}
