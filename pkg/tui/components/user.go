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

// userText/userBg are the adaptive colors for the user message bubble. On dark
// terminals the bubble is a soft blue-gray; on light it is a pale blue, both
// with high-contrast text. Mirrors Kocoro's renderUserMessage palette.
var (
	userText = lipgloss.AdaptiveColor{Light: "#102A43", Dark: "#E6EEF8"}
	userBg   = lipgloss.AdaptiveColor{Light: "#DCE8F5", Dark: "#243447"}
)

func (c *UserComponent) Render(width int, expanded bool) string {
	bar := lipgloss.NewStyle().
		Background(userBg).
		Foreground(userText).
		Width(width).
		Padding(0, 1)
	var sb strings.Builder
	sb.WriteString(bar.Render("› " + c.raw))
	if len(c.attachments) > 0 {
		sb.WriteString("\n")
		sb.WriteString(styleDim().Render("↳ " + strings.Join(c.attachments, ", ")))
	}
	return sb.String()
}
