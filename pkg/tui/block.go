package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

type blockKind int

const (
	blockUser blockKind = iota
	blockAssistant
	blockTool
	blockSystem
)

// block is one rendered unit of conversation history.
type block struct {
	kind blockKind
	id   string // message ID or tool-call ID (for in-place updates)
	raw  string // user text / assistant markdown / tool result content

	// user extras
	attachments []string // @file mention basenames shown under the bar

	// assistant extras
	thinking string
	usage    *blockUsage

	// tool extras
	toolName    string
	toolArgs    string
	toolErr     bool
	toolStart   time.Time
	toolRunning bool
	elapsed     time.Duration
	toolResult  string // full tool output shown in the Ctrl+O expanded view
}

type blockUsage struct {
	inputTokens  int
	outputTokens int
	totalTokens  int
	cost         float64
}

// render returns the styled string for this block at the given width. expanded
// (Ctrl+O view) reveals the full thinking trace on assistant blocks and the
// args/result body on tool blocks.
func (b block) render(width int, expanded bool) string {
	switch b.kind {
	case blockUser:
		bar := lipgloss.NewStyle().
			Background(colorUserBar).
			Foreground(colorSecondary).
			Width(width).
			Padding(0, 1)
		var sb strings.Builder
		sb.WriteString(bar.Render("› " + b.raw))
		if len(b.attachments) > 0 {
			sb.WriteString("\n")
			seg := "↳ " + strings.Join(b.attachments, ", ")
			sb.WriteString(styleDim().Render(seg))
		}
		return sb.String()
	case blockAssistant:
		var sb strings.Builder
		if b.thinking != "" {
			sb.WriteString(renderThinking(b.thinking, expanded))
			sb.WriteString("\n\n")
		}
		sb.WriteString(renderMarkdownCached(b.raw, width))
		if b.usage != nil {
			sb.WriteString("\n")
			sb.WriteString(styleDim().Render(fmt.Sprintf(" · %d tokens · $%.4f", b.usage.totalTokens, b.usage.cost)))
		}
		return sb.String()
	case blockTool:
		elapsed := b.elapsed
		if b.toolRunning {
			elapsed = time.Since(b.toolStart)
		}
		full := b.toolResult
		if full == "" {
			full = b.raw
		}
		preview := toolPreview(full, 3, 80)
		if expanded {
			return formatExpandedToolResult(b.toolName, b.toolArgs, b.toolErr, full, elapsed, b.toolRunning)
		}
		return formatCompactToolResult(b.toolName, b.toolArgs, b.toolErr, preview, elapsed, b.toolRunning)
	default: // blockSystem
		return styleDim().Italic(true).Render(b.raw)
	}
}

// renderThinking renders the assistant's reasoning trace. Compact mode shows a
// dimmed one-line preview (whitespace collapsed, clipped to 200 cells); expanded
// mode (Ctrl+O) shows the full multi-line trace under a "Thinking:" header.
func renderThinking(thinking string, expanded bool) string {
	style := styleDim().Italic(true)
	if !expanded {
		preview := strings.Join(strings.Fields(thinking), " ")
		return style.Render("Thinking: " + truncate(preview, 200))
	}
	var sb strings.Builder
	sb.WriteString(style.Render("Thinking:"))
	for _, ln := range strings.Split(strings.TrimRight(thinking, "\n"), "\n") {
		sb.WriteString("\n")
		sb.WriteString(style.Render("  " + ln))
	}
	return sb.String()
}
