package markdown

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

// CodeBlockNode renders a fenced code block with syntax highlighting.
type CodeBlockNode struct {
	Lang    string
	Content string
	cache   map[string]string
}

func (n *CodeBlockNode) Height(width int) int {
	return lipgloss.Height(n.Render(width))
}

func (n *CodeBlockNode) Render(width int) string {
	if n.cache == nil {
		n.cache = make(map[string]string)
	}
	key := fmt.Sprintf("%d:%s", width, n.Lang)
	if out, ok := n.cache[key]; ok {
		return out
	}
	out := renderCodeBlock(n.Lang, n.Content, width)
	n.cache[key] = out
	return out
}
