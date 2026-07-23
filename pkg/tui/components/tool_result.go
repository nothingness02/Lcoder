package components

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ToolResultComponent renders a single tool call result with a compact row and
// a Ctrl+O expanded view.
type ToolResultComponent struct {
	id        string
	toolName  string
	toolArgs  string
	result    string
	isError   bool
	running   bool
	toolStart time.Time
	elapsed   time.Duration
	expanded  bool
}

// NewToolResultComponent creates a fully initialized tool result component.
func NewToolResultComponent(id, toolName, toolArgs, result string, isError bool, running bool, toolStart time.Time, elapsed time.Duration) *ToolResultComponent {
	return &ToolResultComponent{
		id:        id,
		toolName:  toolName,
		toolArgs:  toolArgs,
		result:    result,
		isError:   isError,
		running:   running,
		toolStart: toolStart,
		elapsed:   elapsed,
	}
}

func (c *ToolResultComponent) ID() string      { return c.id }
func (c *ToolResultComponent) Kind() BlockKind { return BlockTool }
func (c *ToolResultComponent) Expanded() bool  { return c.expanded }
func (c *ToolResultComponent) SetExpanded(v bool) {
	c.expanded = v
}

func (c *ToolResultComponent) Height(width int, expanded bool) int {
	return lipgloss.Height(c.Render(width, expanded))
}

func (c *ToolResultComponent) Render(width int, expanded bool) string {
	elapsed := c.elapsed
	if c.running {
		elapsed = time.Since(c.toolStart)
	}
	if expanded || c.expanded {
		return formatExpandedToolResult(c.toolName, c.toolArgs, c.isError, c.result, elapsed, c.running, width)
	}
	preview := toolPreview(c.result, 2, 1, width)
	return formatCompactToolResult(c.toolName, c.toolArgs, c.isError, preview, elapsed, c.running)
}

// Update handles local interaction messages.
func (c *ToolResultComponent) Update(msg tea.Msg) (BlockComponent, tea.Cmd) {
	switch msg.(type) {
	case ToggleExpandedMsg:
		c.expanded = !c.expanded
	}
	return c, nil
}
