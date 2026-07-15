package markdown

import (
	"sync"

	"github.com/charmbracelet/lipgloss"
)

// cacheKey identifies a previously-rendered code block.
type cacheKey struct {
	width   int
	lang    string
	content string
}

// CodeBlockNode renders a fenced code block with syntax highlighting.
type CodeBlockNode struct {
	Lang    string
	Content string
	cache   map[cacheKey]string
	mu      sync.RWMutex
}

func (n *CodeBlockNode) Height(width int) int {
	return lipgloss.Height(n.Render(width))
}

func (n *CodeBlockNode) Render(width int) string {
	key := cacheKey{width: width, lang: n.Lang, content: n.Content}

	n.mu.RLock()
	if out, ok := n.cache[key]; ok {
		n.mu.RUnlock()
		return out
	}
	n.mu.RUnlock()

	out := renderCodeBlock(n.Lang, n.Content, width)

	n.mu.Lock()
	if n.cache == nil {
		n.cache = make(map[cacheKey]string)
	}
	if _, ok := n.cache[key]; !ok {
		n.cache[key] = out
	}
	n.mu.Unlock()
	return out
}
