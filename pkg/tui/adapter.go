package tui

import "strings"

// toComponent converts an internal data block into a renderable component.
// For now all kinds fall back to the legacy block renderer; System/User
// components will be introduced in the next task.
func toComponent(b block) BlockComponent {
	return fallbackComponent{b: b}
}

// componentsFromBlocks converts a slice of blocks in order.
func componentsFromBlocks(blocks []block) []BlockComponent {
	out := make([]BlockComponent, len(blocks))
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
func (f fallbackComponent) Kind() BlockKind                { return f.b.kind }
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
