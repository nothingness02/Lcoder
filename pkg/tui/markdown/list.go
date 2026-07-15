package markdown

import (
	"fmt"
	"strings"
)

// ListNode renders ordered or unordered lists.
type ListNode struct {
	Ordered bool
	Items   []string
}

func (n *ListNode) Height(width int) int {
	return len(n.Items)
}

func (n *ListNode) Render(width int) string {
	var sb strings.Builder
	for i, item := range n.Items {
		prefix := "• "
		if n.Ordered {
			prefix = fmt.Sprintf("%d. ", i+1)
		}
		sb.WriteString(prefix + item + "\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}
