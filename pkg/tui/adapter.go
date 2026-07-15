package tui

import (
	"strings"

	"github.com/lcoder/lcoder/pkg/tui/components"
)

// toComponent converts an internal data block into a renderable component.
func toComponent(b block) components.BlockComponent {
	switch b.kind {
	case components.BlockSystem:
		return components.NewSystemLogComponent(b.id, b.raw)
	case components.BlockUser:
		return components.NewUserComponent(b.id, b.raw, b.attachments)
	default:
		return fallbackComponent{b: b}
	}
}

// componentsFromBlocks converts a slice of blocks in order.
func componentsFromBlocks(blocks []block) []components.BlockComponent {
	out := make([]components.BlockComponent, len(blocks))
	for i, b := range blocks {
		out[i] = toComponent(b)
	}
	return out
}

// fallbackComponent carries not-yet-componentized blocks during migration.
type fallbackComponent struct {
	b block
}

func (f fallbackComponent) ID() string                     { return f.b.id }
func (f fallbackComponent) Kind() components.BlockKind     { return f.b.kind }
func (f fallbackComponent) Height(width int, expanded bool) int {
	lines := len(splitLines(f.b.render(width, expanded)))
	if lines == 0 {
		return 1
	}
	return lines
}
func (f fallbackComponent) Render(width int, expanded bool) string { return f.b.render(width, expanded) }

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
