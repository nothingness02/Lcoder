package markdown

import "github.com/charmbracelet/lipgloss"

// TextNode renders a plain paragraph or text block.
type TextNode struct {
	Text string
}

func (n *TextNode) Height(width int) int {
	return lipgloss.Height(n.Render(width))
}

func (n *TextNode) Render(width int) string {
	return widthStyle(width).Render(n.Text)
}
