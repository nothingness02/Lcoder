package components

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const spinnerInterval = 100 * time.Millisecond

var spinnerGlyphs = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// toolFriendlyLabels maps built-in tool names to human-friendly descriptions.
var toolFriendlyLabels = map[string]string{
	"bash":        "Running a command",
	"read":        "Reading a file",
	"write":       "Writing a file",
	"edit":        "Editing a file",
	"grep":        "Searching in files",
	"find":        "Finding files",
	"ls":          "Listing files",
	"memory":      "Updating memory",
	"subagent":    "Running a subagent",
	"tool_search": "Searching for tools",
}

// friendlyToolLabel returns a human-friendly label for a tool name, falling back
// to the raw name when no mapping exists.
func friendlyToolLabel(name string) string {
	if label, ok := toolFriendlyLabels[name]; ok {
		return label
	}
	return name
}

// formatToolCallLabel builds a compact header label from the tool name and its
// key argument.
func formatToolCallLabel(name, keyArg string) string {
	label := friendlyToolLabel(name)
	switch {
	case label != name && keyArg != "":
		return label + ": " + keyArg
	case label != name:
		return label
	default:
		return fmt.Sprintf("%s(%s)", name, keyArg)
	}
}

// toolResultBrief returns a short elapsed-time string for completed tools.
func toolResultBrief(elapsed time.Duration) string {
	if elapsed > 100*time.Millisecond {
		return fmt.Sprintf("%.1fs", elapsed.Seconds())
	}
	return ""
}

// collapseToolOutput returns a head/tail sample of content: the first head
// lines and last tail lines with an elision marker between when content is
// longer. Each line is clipped to maxWidth, and the whole sample obeys a
// character budget of one line-share (maxWidth-6, floored at 20) per sampled
// line, so a minified-JSON wall can't dominate the transcript. Keeping the
// tail matters for logs and stack traces, where the salient error is usually
// at the end. (opencode's collapse-tool-output shape.)
func collapseToolOutput(content string, head, tail, maxWidth int) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	if head+tail < len(lines) {
		omitted := len(lines) - head - tail
		sampled := make([]string, 0, head+tail+1)
		sampled = append(sampled, lines[:head]...)
		sampled = append(sampled, fmt.Sprintf("… +%d more (ctrl+o to expand)", omitted))
		sampled = append(sampled, lines[len(lines)-tail:]...)
		lines = sampled
	}
	share := max(20, maxWidth-6)
	for i, ln := range lines {
		lines[i] = truncate(ln, min(maxWidth, share))
	}
	return strings.Join(lines, "\n")
}

// runningGlyph returns an accent-colored spinner frame that animates by wall
// clock so in-flight tool rows feel alive without plumbing the global spinner
// frame through every block.
func runningGlyph() string {
	idx := int(time.Now().UnixNano()/int64(spinnerInterval)) % len(spinnerGlyphs)
	return styleAccent().Render(spinnerGlyphs[idx])
}

// formatCompactToolResult renders the collapsed tool row. When running is true
// the row shows a live spinner and elapsed time instead of a completion icon.
// When the tool has finished, a short preview of the output is shown beneath the
// header; Ctrl+O expands to the full output.
func formatCompactToolResult(toolName, args string, isError bool, preview string, elapsed time.Duration, running bool) string {
	keyArg := toolKeyArg(toolName, args)
	label := formatToolCallLabel(toolName, keyArg)
	dimStyle := styleDim()

	if running {
		line := fmt.Sprintf(" %s", label)
		if elapsed > 100*time.Millisecond {
			line += fmt.Sprintf("  %.1fs", elapsed.Seconds())
		}
		return runningGlyph() + dimStyle.Render(line)
	}

	icon := styleSuccess().Render("✓")
	if isError {
		icon = styleError().Render("✗")
	}
	brief := toolResultBrief(elapsed)
	line := fmt.Sprintf("⏵ %s  %s", label, icon)
	if brief != "" {
		line += "  " + brief
	}

	var sb strings.Builder
	sb.WriteString(dimStyle.Render(line))
	if preview != "" {
		for _, ln := range strings.Split(preview, "\n") {
			sb.WriteString("\n")
			sb.WriteString(dimStyle.Render("  │ " + ln))
		}
	}
	return sb.String()
}

// formatExpandedToolResult renders the Ctrl+O expanded view with the full tool
// arguments and output. Edit calls render their change as a full clustered diff
// built from the arguments instead of raw JSON; write calls render the same
// diff on overwrite (old content from result details) or the full highlighted
// content for new files. No head/tail truncation is applied so the user can
// inspect everything the tool returned; width clips the body lines.
func formatExpandedToolResult(toolName, args string, details map[string]any, isError bool, content string, elapsed time.Duration, running bool, width int) string {
	dimStyle := styleDim()
	bodyStyle := dimStyle
	if isError {
		bodyStyle = styleError()
	}

	var sb strings.Builder

	// Header line mirrors the compact row but without a preview.
	keyArg := toolKeyArg(toolName, args)
	label := formatToolCallLabel(toolName, keyArg)
	icon := styleSuccess().Render("✓")
	if isError {
		icon = styleError().Render("✗")
	} else if running {
		icon = runningGlyph()
	}
	header := fmt.Sprintf("⏵ %s  %s", label, icon)
	if brief := toolResultBrief(elapsed); brief != "" {
		header += "  " + brief
	}
	sb.WriteString(dimStyle.Render(header))

	// For edit calls the interesting payload is the change itself; render it as
	// a clustered diff built from the arguments rather than echoing raw JSON.
	if toolName == "edit" {
		if rows := EditDiffRows(args, 0, diffExpandHint); len(rows) > 0 {
			sb.WriteString("\n")
			sb.WriteString(dimStyle.Render("  Changes:"))
			for _, ln := range rows {
				sb.WriteString("\n")
				sb.WriteString("    " + ln)
			}
		}
	} else if toolName == "write" {
		// Overwrite with the previous content in the details: full diff.
		// Otherwise the full written content, syntax highlighted.
		if old, ok := details[oldContentDetailKey].(string); ok && old != "" {
			if rows := RenderClusteredDiff(old, WriteContentFromArgs(args), 0, diffExpandHint); len(rows) > 0 {
				sb.WriteString("\n")
				sb.WriteString(dimStyle.Render("  Changes:"))
				for _, ln := range rows {
					sb.WriteString("\n")
					sb.WriteString("    " + ln)
				}
			}
		} else if rows := WriteContentRows(WriteContentFromArgs(args), writePathFromArgs(args), 0, diffExpandHint); len(rows) > 0 {
			sb.WriteString("\n")
			sb.WriteString(dimStyle.Render("  Content:"))
			for _, ln := range rows {
				sb.WriteString("\n")
				sb.WriteString("    " + ln)
			}
		}
	} else if args != "" {
		// Arguments section with pretty-printed JSON when possible.
		sb.WriteString("\n")
		sb.WriteString(dimStyle.Render("  Arguments:"))
		for _, ln := range strings.Split(formatArgsForDisplay(args), "\n") {
			sb.WriteString("\n")
			sb.WriteString(dimStyle.Render("    " + ln))
		}
	}

	// Output section: full, untruncated result.
	if content != "" {
		sb.WriteString("\n")
		label := "  Output:"
		if isError {
			label = "  Error:"
		}
		sb.WriteString(dimStyle.Render(label))
		content = strings.TrimRight(content, "\n")
		for _, ln := range strings.Split(content, "\n") {
			sb.WriteString("\n")
			sb.WriteString(bodyStyle.Render("    " + ln))
		}
	}

	return sb.String()
}

// formatArgsForDisplay returns a human-readable rendering of a tool's JSON
// arguments. Valid JSON is pretty-printed; otherwise the raw string is returned
// truncated to a generous limit.
func formatArgsForDisplay(args string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(args), &m); err != nil {
		return truncate(args, 500)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return truncate(args, 500)
	}
	return string(data)
}

// toolKeyArg extracts the most meaningful argument from a tool's JSON args.
func toolKeyArg(toolName string, argsJSON string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &m); err != nil {
		return truncate(argsJSON, 40)
	}
	var key string
	switch toolName {
	case "bash":
		key = strVal(m, "command")
	case "read", "write", "edit", "ls":
		key = strVal(m, "path")
	case "grep", "find":
		key = strVal(m, "pattern")
		if path := strVal(m, "path"); path != "" {
			key += ", " + path
		}
	default:
		for _, f := range []string{"query", "path", "url", "command", "name", "pattern"} {
			if v := strVal(m, f); v != "" {
				key = v
				break
			}
		}
	}
	if key == "" {
		return truncate(argsJSON, 40)
	}
	return truncate(key, 50)
}

func strVal(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}
