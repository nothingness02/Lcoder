package components

import "github.com/charmbracelet/lipgloss"

// SystemLogComponent renders a dim, italic system line.
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
	return styleDim().Italic(true).Render(c.raw)
}
