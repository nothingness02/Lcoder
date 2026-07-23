package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// SystemLogComponent renders a system line. Callers express severity through
// their own styling (StyleError / StyleWarn / StyleInfo / StyleDim); text that
// already carries ANSI styling is rendered as-is so an error line stays red
// instead of being flattened to one dim-italic look. Unstyled text falls back
// to the dim-italic info baseline (e.g. system lines rebuilt from a reloaded
// session, which carry no styling).
type SystemLogComponent struct {
	id  string
	raw string
}

func NewSystemLogComponent(id, raw string) *SystemLogComponent {
	return &SystemLogComponent{id: id, raw: raw}
}

func (c *SystemLogComponent) ID() string      { return c.id }
func (c *SystemLogComponent) Kind() BlockKind { return BlockSystem }

func (c *SystemLogComponent) Height(width int, expanded bool) int {
	if c.raw == "" {
		return 0
	}
	return lipgloss.Height(c.Render(width, expanded))
}

func (c *SystemLogComponent) Render(width int, expanded bool) string {
	if c.raw == "" {
		return ""
	}
	if strings.ContainsRune(c.raw, '\x1b') {
		return c.raw
	}
	return styleDim().Italic(true).Render(c.raw)
}
