package markdown

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ListNode renders ordered or unordered lists.
type ListNode struct {
	Ordered bool
	Items   []string
}

func (n *ListNode) Height(width int) int {
	return lipgloss.Height(n.Render(width))
}

func (n *ListNode) Render(width int) string {
	var sb strings.Builder
	style := widthStyle(width)
	for i, item := range n.Items {
		prefix := "• "
		if n.Ordered {
			prefix = fmt.Sprintf("%d. ", i+1)
		}
		sb.WriteString(style.Render(prefix + item))
		if i < len(n.Items)-1 {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}
