package components

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/lcoder/lcoder/pkg/tui/markdown"
)

// UsageInfo carries token and cost metadata for an assistant message.
type UsageInfo struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	Cost         float64
}

// AssistantComponent renders an assistant message, optional thinking trace,
// and usage metadata using glamour for full markdown rendering.
type AssistantComponent struct {
	id       string
	thinking string
	content  string
	usage    *UsageInfo
	expanded bool

	// rendered cache keyed by width + content to avoid re-rendering glamour on
	// every frame.
	cachedRenderWidth   int
	cachedRenderContent string
	cachedRender        string
}

// NewAssistantComponent creates a fully initialized assistant component.
func NewAssistantComponent(id, thinking, content string, usage *UsageInfo) *AssistantComponent {
	return &AssistantComponent{
		id:       id,
		thinking: thinking,
		content:  content,
		usage:    usage,
	}
}

func (c *AssistantComponent) ID() string      { return c.id }
func (c *AssistantComponent) Kind() BlockKind { return BlockAssistant }
func (c *AssistantComponent) Expanded() bool  { return c.expanded }
func (c *AssistantComponent) SetExpanded(v bool) {
	c.expanded = v
}

func (c *AssistantComponent) Height(width int, expanded bool) int {
	return lipgloss.Height(c.Render(width, expanded))
}

func (c *AssistantComponent) Render(width int, expanded bool) string {
	effectiveExpanded := expanded || c.expanded
	var sb strings.Builder
	if c.thinking != "" {
		sb.WriteString(c.renderThinking(effectiveExpanded))
		if c.content != "" {
			sb.WriteString("\n\n")
		}
	}
	if c.content != "" {
		sb.WriteString(c.renderedContent(width))
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

// SetContent replaces the streamed content and invalidates the render cache.
func (c *AssistantComponent) SetContent(content string) {
	c.content = content
	c.cachedRenderContent = ""
}

// SetThinking replaces the reasoning trace.
func (c *AssistantComponent) SetThinking(thinking string) {
	c.thinking = thinking
}

// renderedContent returns the glamour-rendered markdown for the current width.
func (c *AssistantComponent) renderedContent(width int) string {
	if c.cachedRenderWidth == width && c.cachedRenderContent == c.content {
		return c.cachedRender
	}
	out := markdown.RenderMarkdown(c.content, width)
	c.cachedRenderWidth = width
	c.cachedRenderContent = c.content
	c.cachedRender = out
	return out
}

// renderThinking renders the assistant's reasoning trace. Collapsed mode shows
// a single dim indicator line; expanded mode shows the full multi-line trace
// under a "Thinking:" header.
func (c *AssistantComponent) renderThinking(expanded bool) string {
	style := styleDim().Italic(true)
	if !expanded {
		return style.Render("Thinking…")
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
		rw := lipgloss.Width(string(r))
		if w+rw > width-1 {
			break
		}
		w += rw
		runes = append(runes, r)
	}
	return string(runes) + "…"
}
