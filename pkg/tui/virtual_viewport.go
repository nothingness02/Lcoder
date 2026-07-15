package tui

import (
	"strings"

	"github.com/lcoder/lcoder/pkg/tui/components"
)

// componentLayout records where a component starts in the virtual scroll space.
type componentLayout struct {
	comp   components.BlockComponent
	offset int
	height int
}

// layoutComponents computes height and offset for every component.
func layoutComponents(comps []components.BlockComponent, width int, expanded bool) []componentLayout {
	layouts := make([]componentLayout, len(comps))
	y := 0
	for i, comp := range comps {
		h := comp.Height(width, expanded)
		layouts[i] = componentLayout{comp: comp, offset: y, height: h}
		y += h
	}
	return layouts
}

// buildVirtualContent renders only the components that intersect the visible
// window. Off-screen components are replaced with blank lines so the total
// height and scroll position remain consistent.
func buildVirtualContent(comps []components.BlockComponent, width, height, scrollY int, expanded bool) string {
	if height <= 0 {
		return ""
	}
	layouts := layoutComponents(comps, width, expanded)
	startLine := scrollY
	endLine := scrollY + height

	var sb strings.Builder
	for _, layout := range layouts {
		compStart := layout.offset
		compEnd := layout.offset + layout.height
		if compEnd <= startLine || compStart >= endLine {
			// Off-screen: emit blank lines to preserve total height.
			for range layout.height {
				sb.WriteString("\n")
			}
			continue
		}
		rendered := layout.comp.Render(width, expanded)
		sb.WriteString(rendered)
		if !strings.HasSuffix(rendered, "\n") {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}
