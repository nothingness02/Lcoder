package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/lcoder/lcoder/pkg/tui/components"
)

// componentLayout records where a component starts in the virtual scroll space.
type componentLayout struct {
	comp   components.BlockComponent
	offset int
	height int
	width  int
}

// blockFocusStyle adds a single-cell left border to the focused component. The
// content is rendered at width-1 so the total width stays unchanged.
var blockFocusStyle = lipgloss.NewStyle().
	BorderStyle(lipgloss.Border{Left: "┃"}).
	BorderLeft(true).
	BorderForeground(colorSelect)

// layoutComponents computes height and offset for every component.
func layoutComponents(comps []components.BlockComponent, width int, expanded bool, focusedIndex int) []componentLayout {
	layouts := make([]componentLayout, len(comps))
	y := 0
	for i, comp := range comps {
		w := width
		if i == focusedIndex {
			w = width - 1
			if w < 0 {
				w = 0
			}
		}
		h := comp.Height(w, expanded)
		layouts[i] = componentLayout{comp: comp, offset: y, height: h, width: width}
		y += h
	}
	return layouts
}

// buildVirtualContent renders only the components that intersect the visible
// window. Off-screen components are replaced with blank lines so the total
// height and scroll position remain consistent.
func buildVirtualContent(layouts []componentLayout, height, scrollY int, expanded bool, focusedIndex int) string {
	if height <= 0 {
		return ""
	}
	startLine := scrollY
	endLine := scrollY + height

	allLines := make([]string, 0)
	for i, layout := range layouts {
		compStart := layout.offset
		compEnd := layout.offset + layout.height
		if compEnd <= startLine || compStart >= endLine {
			// Off-screen: emit blank lines to preserve total height.
			for range layout.height {
				allLines = append(allLines, "")
			}
			continue
		}
		contentWidth := layout.width
		if i == focusedIndex {
			contentWidth = layout.width - 1
			if contentWidth < 0 {
				contentWidth = 0
			}
		}
		rendered := strings.TrimRight(layout.comp.Render(contentWidth, expanded), "\n")
		lines := strings.Split(rendered, "\n")
		if i == focusedIndex {
			for j := range lines {
				lines[j] = blockFocusStyle.Render(lines[j])
			}
		}
		for len(lines) < layout.height {
			lines = append(lines, "")
		}
		if len(lines) > layout.height {
			lines = lines[:layout.height]
		}
		allLines = append(allLines, lines...)
	}
	return strings.Join(allLines, "\n")
}

// clamp restricts v to the inclusive range [lo, hi].
func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// maxTotalHeight returns the sum of all layout heights.
func maxTotalHeight(layouts []componentLayout) int {
	total := 0
	for _, ly := range layouts {
		total += ly.height
	}
	return total
}
