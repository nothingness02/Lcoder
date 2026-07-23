package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/sahilm/fuzzy"
)

// commandEntry and commandRegistry now live in slash_registry.go so that
// commands can carry an executable Handler and be extended at runtime.

// menuMatch pairs a command with the fuzzy-matched rune positions for highlight.
type menuMatch struct {
	entry          commandEntry
	matchedIndexes []int
}

// menuMatches returns ranked commands for a query (no leading slash). Exact
// prefix matches sort first, then fuzzy matches by score.
func menuMatches(query string) []menuMatch {
	query = strings.TrimPrefix(strings.TrimSpace(query), "/")
	if query == "" {
		out := make([]menuMatch, len(commandRegistry))
		for i, e := range commandRegistry {
			out[i] = menuMatch{entry: e}
		}
		return out
	}

	var prefix, rest []menuMatch
	names := make([]string, len(commandRegistry))
	for i, e := range commandRegistry {
		names[i] = e.Name
	}
	seen := map[string]bool{}

	for _, e := range commandRegistry {
		if strings.HasPrefix(e.Name, query) {
			n := len(query)
			idx := make([]int, n)
			for i := range idx {
				idx[i] = i
			}
			prefix = append(prefix, menuMatch{entry: e, matchedIndexes: idx})
			seen[e.Name] = true
		}
	}

	// Fuzzy subsequence fallback only kicks in for ≥3 chars; below that a
	// subsequence match is mostly noise (single/double chars hit everywhere),
	// so short queries stay prefix-only.
	if len(query) >= 3 {
		for _, fm := range fuzzy.Find(query, names) {
			e := commandRegistry[fm.Index]
			if seen[e.Name] {
				continue
			}
			rest = append(rest, menuMatch{entry: e, matchedIndexes: fm.MatchedIndexes})
		}
	}
	return append(prefix, rest...)
}

// dropListSize is the fixed number of visible rows in the command dropdown.
// The box height stays constant as the match count changes, so the layout
// doesn't jump while typing; the selection scrolls within the window.
const dropListSize = 8

// renderMenu draws the dropdown with the selected row highlighted and matched
// characters emphasized. It renders a fixed-height sliding window over the
// matches so the box keeps a constant row count regardless of match count.
func renderMenu(matches []menuMatch, selected, width int) string {
	if len(matches) == 0 {
		return ""
	}
	visible := len(matches)
	if visible > dropListSize {
		visible = dropListSize
	}
	start := 0
	if selected >= dropListSize {
		start = selected - dropListSize + 1
	}
	if start+visible > len(matches) {
		start = len(matches) - visible
	}
	if start < 0 {
		start = 0
	}

	var lines []string
	for i := start; i < start+visible; i++ {
		m := matches[i]
		name := highlightMatch(m.entry.Name, m.matchedIndexes)
		desc := styleDim().Render("  " + m.entry.Description)
		row := "/" + name + desc
		if i == selected {
			row = lipgloss.NewStyle().Foreground(colorSelect).Render("› ") + row
		} else {
			row = "  " + row
		}
		lines = append(lines, truncateCells(row, width, "…"))
	}
	// Pad to a fixed row count so the box height is constant even when the
	// current window holds fewer than dropListSize matches.
	for len(lines) < dropListSize {
		lines = append(lines, "")
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorFaint)
	return box.Render(strings.Join(lines, "\n"))
}

// highlightMatch bolds matched rune positions in name.
func highlightMatch(name string, idx []int) string {
	if len(idx) == 0 {
		return name
	}
	set := map[int]bool{}
	for _, i := range idx {
		set[i] = true
	}
	var sb strings.Builder
	for i, r := range name {
		if set[i] {
			sb.WriteString(styleAccent().Bold(true).Render(string(r)))
		} else {
			sb.WriteString(string(r))
		}
	}
	return sb.String()
}
