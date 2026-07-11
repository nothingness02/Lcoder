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

	for _, fm := range fuzzy.Find(query, names) {
		e := commandRegistry[fm.Index]
		if seen[e.Name] {
			continue
		}
		rest = append(rest, menuMatch{entry: e, matchedIndexes: fm.MatchedIndexes})
	}
	return append(prefix, rest...)
}

// renderMenu draws the dropdown with the selected row highlighted and matched
// characters emphasized.
func renderMenu(matches []menuMatch, selected, width int) string {
	if len(matches) == 0 {
		return ""
	}
	var lines []string
	for i, m := range matches {
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
