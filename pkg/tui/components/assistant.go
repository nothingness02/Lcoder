package components

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/lcoder/lcoder/pkg/tui/markdown"
	"github.com/mattn/go-runewidth"
)

// UsageInfo carries token and cost metadata for an assistant message.
type UsageInfo struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	Cost         float64
}

// AssistantComponent renders an assistant message, optional thinking trace,
// and usage metadata using the markdown node tree.
type AssistantComponent struct {
	id       string
	thinking string
	content  string
	nodes    []markdown.Node
	usage    *UsageInfo
	expanded bool
}

// NewAssistantComponent creates a fully initialized assistant component.
func NewAssistantComponent(id, thinking, content string, usage *UsageInfo) *AssistantComponent {
	return &AssistantComponent{
		id:       id,
		thinking: thinking,
		content:  content,
		nodes:    markdown.Parse(content),
		usage:    usage,
	}
}

func (c *AssistantComponent) ID() string      { return c.id }
func (c *AssistantComponent) Kind() BlockKind { return BlockAssistant }

func (c *AssistantComponent) Height(width int, expanded bool) int {
	effectiveExpanded := expanded || c.expanded
	return lipgloss.Height(c.Render(width, effectiveExpanded))
}

func (c *AssistantComponent) Render(width int, expanded bool) string {
	effectiveExpanded := expanded || c.expanded
	var sb strings.Builder
	if c.thinking != "" {
		sb.WriteString(c.renderThinking(effectiveExpanded))
		sb.WriteString("\n\n")
	}
	for i, n := range c.nodes {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(n.Render(width))
	}
	if c.usage != nil {
		sb.WriteString("\n")
		sb.WriteString(styleDim().Render(fmt.Sprintf(" · %d tokens · $%.4f", c.usage.TotalTokens, c.usage.Cost)))
	}
	return sb.String()
}

// Update handles local interaction messages.
func (c *AssistantComponent) Update(msg tea.Msg) (BlockComponent, tea.Cmd) {
	switch msg.(type) {
	case ToggleExpandedMsg:
		c.expanded = !c.expanded
	}
	return c, nil
}

// SetContent replaces the streamed content and re-parses the markdown tree.
func (c *AssistantComponent) SetContent(content string) {
	c.content = content
	c.nodes = markdown.Parse(content)
}

// renderThinking renders the assistant's reasoning trace. Compact mode shows a
// dimmed one-line preview (whitespace collapsed, clipped to 200 cells);
// expanded mode shows the full multi-line trace under a "Thinking:" header.
func (c *AssistantComponent) renderThinking(expanded bool) string {
	style := styleDim().Italic(true)
	if !expanded {
		preview := strings.Join(strings.Fields(c.thinking), " ")
		return style.Render("Thinking: " + truncate(preview, 200))
	}
	var sb strings.Builder
	sb.WriteString(style.Render("Thinking:"))
	for ln := range strings.SplitSeq(strings.TrimRight(c.thinking, "\n"), "\n") {
		sb.WriteString("\n")
		sb.WriteString(style.Render("  " + ln))
	}
	return sb.String()
}

// truncate clips s to at most width cells, appending an ellipsis when it does
// not fit. It mirrors the legacy behavior used by the old block renderer.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	var runes []rune
	w := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if w+rw > width-1 {
			break
		}
		w += rw
		runes = append(runes, r)
	}
	return string(runes) + "…"
}
