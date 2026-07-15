package markdown

import "github.com/charmbracelet/lipgloss"

// HeadingNode renders a markdown heading.
type HeadingNode struct {
	Level int
	Text  string
}

func (n *HeadingNode) Height(width int) int {
	return lipgloss.Height(n.Render(width))
}

func (n *HeadingNode) Render(width int) string {
	style := widthStyle(width).Bold(true)
	if n.Level == 1 {
		style = style.Italic(true).Underline(true)
	}
	return style.Render(n.Text)
}
