package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// UserComponent renders a user message bar and @file attachments.
type UserComponent struct {
	id          string
	raw         string
	attachments []string
}

func NewUserComponent(id, raw string, attachments []string) *UserComponent {
	return &UserComponent{id: id, raw: raw, attachments: attachments}
}

func (c *UserComponent) ID() string      { return c.id }
func (c *UserComponent) Kind() BlockKind { return BlockUser }

func (c *UserComponent) Height(width int, expanded bool) int {
	return lipgloss.Height(c.Render(width, expanded))
}

func (c *UserComponent) Render(width int, expanded bool) string {
	bar := lipgloss.NewStyle().
		Background(lipgloss.Color("237")).
		Foreground(lipgloss.Color("252")).
		Width(width).
		Padding(0, 1)
	var sb strings.Builder
	sb.WriteString(bar.Render("> " + c.raw))
	if len(c.attachments) > 0 {
		sb.WriteString("\n")
		dim := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
		sb.WriteString(dim.Render("↳ " + strings.Join(c.attachments, ", ")))
	}
	return sb.String()
}
