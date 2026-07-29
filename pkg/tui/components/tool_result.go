package components

import (
	"fmt"
	"strings"
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

	// subagent activity mirrored from a child agent, rendered nested under
	// the call (kimi-code's nested subagent display).
	subagentLines []string
	subagentTail  string
	subagentLive  bool

	// chip is a compact result statistic shown in the header (e.g. "12 lines").
	chip string

	// subagentChildren are per-child rows for the group display.
	subagentChildren []SubagentChildRow
}

// SubagentChildRow is one child agent's status in the group display
// (kimi-code's AgentGroup rows).
type SubagentChildRow struct {
	Profile string
	Status  string // "running" | "completed" | "timeout" | "failed"
	Tools   int
	Started time.Time
	Elapsed time.Duration
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

// SetSubagentActivity attaches mirrored child-agent activity for nested
// rendering under this tool call.
func (c *ToolResultComponent) SetSubagentActivity(lines []string, tail string, live bool) {
	c.subagentLines = lines
	c.subagentTail = tail
	c.subagentLive = live
}

// SetChip attaches a compact result statistic for the header.
func (c *ToolResultComponent) SetChip(chip string) { c.chip = chip }

// SetSubagentChildren attaches per-child rows for the group display.
func (c *ToolResultComponent) SetSubagentChildren(children []SubagentChildRow) {
	c.subagentChildren = children
}

func (c *ToolResultComponent) Height(width int, expanded bool) int {
	return lipgloss.Height(c.Render(width, expanded))
}

func (c *ToolResultComponent) Render(width int, expanded bool) string {
	elapsed := c.elapsed
	if c.running {
		elapsed = time.Since(c.toolStart)
	}
	var base string
	if expanded || c.expanded {
		base = formatExpandedToolResult(c.toolName, c.toolArgs, c.isError, c.result, elapsed, c.running, width)
	} else {
		preview := toolPreview(c.result, 2, 1, width)
		base = formatCompactToolResult(c.toolName, c.toolArgs, c.isError, preview, elapsed, c.running)
	}
	// Header chip: compact result statistic, e.g. " · 12 lines".
	if c.chip != "" && !c.running {
		if idx := strings.IndexByte(base, '\n'); idx >= 0 {
			base = base[:idx] + "  " + styleDim().Render("· "+c.chip) + base[idx:]
		} else {
			base += "  " + styleDim().Render("· "+c.chip)
		}
	}
	// Bash body leads with the command itself, `$ ` accented (kimi style).
	if c.toolName == "bash" && !c.running {
		if cmd := toolKeyArg(c.toolName, c.toolArgs); cmd != "" {
			if idx := strings.IndexByte(base, '\n'); idx >= 0 {
				cmdLine := "  " + styleAccent().Render("$ ") + styleDim().Render(cmd)
				base = base[:idx+1] + cmdLine + base[idx:]
			}
		}
	}
	if activity := c.renderSubagentActivity(expanded || c.expanded); activity != "" {
		base += "\n" + activity
	}
	return base
}

// subagentCompactMaxLines caps the nested activity shown in the compact row;
// the expanded view shows everything.
const subagentCompactMaxLines = 8

// renderSubagentActivity renders mirrored child-agent activity as dim,
// indented lines nested under the tool call. With two or more children it
// leads with a group header and per-child tree rows (kimi's AgentGroup).
func (c *ToolResultComponent) renderSubagentActivity(expanded bool) string {
	if len(c.subagentLines) == 0 && c.subagentTail == "" && len(c.subagentChildren) == 0 {
		return ""
	}
	lines := make([]string, 0, len(c.subagentLines)+1)
	lines = append(lines, c.subagentLines...)
	if c.subagentTail != "" {
		lines = append(lines, c.subagentTail)
	}
	if !expanded && len(lines) > subagentCompactMaxLines {
		lines = lines[len(lines)-subagentCompactMaxLines:]
	}
	var sb strings.Builder
	header := "  ├ subagent"
	if c.subagentLive {
		header = "  ├ subagent (running)"
	}
	if len(c.subagentChildren) >= 2 {
		header = c.subagentGroupHeader()
	}
	sb.WriteString(styleDim().Render(header))
	if len(c.subagentChildren) >= 2 {
		sb.WriteString(c.subagentGroupRows())
	}
	for _, ln := range lines {
		for _, part := range strings.Split(ln, "\n") {
			sb.WriteString("\n")
			sb.WriteString(styleDim().Render("  │ " + part))
		}
	}
	return sb.String()
}

// subagentGroupHeader renders the aggregate line, e.g.
// "├ 3 subagents (1 done, 2 running)".
func (c *ToolResultComponent) subagentGroupHeader() string {
	done := 0
	running := 0
	for _, ch := range c.subagentChildren {
		if ch.Status == "running" {
			running++
		} else {
			done++
		}
	}
	return fmt.Sprintf("  ├ %d subagents (%d done, %d running)", len(c.subagentChildren), done, running)
}

// subagentGroupRows renders per-child tree rows with profile, tool count,
// elapsed time, and status.
func (c *ToolResultComponent) subagentGroupRows() string {
	var sb strings.Builder
	for i, ch := range c.subagentChildren {
		branch := "├─"
		if i == len(c.subagentChildren)-1 {
			branch = "└─"
		}
		elapsed := ch.Elapsed
		if ch.Status == "running" {
			elapsed = time.Since(ch.Started)
		}
		row := fmt.Sprintf("%s %s · %d tools · %.0fs · %s", branch, ch.Profile, ch.Tools, elapsed.Seconds(), ch.Status)
		sb.WriteString("\n")
		sb.WriteString(styleDim().Render("  │ " + row))
	}
	return sb.String()
}

// Update handles local interaction messages.
func (c *ToolResultComponent) Update(msg tea.Msg) (BlockComponent, tea.Cmd) {
	switch msg.(type) {
	case ToggleExpandedMsg:
		c.expanded = !c.expanded
	}
	return c, nil
}
