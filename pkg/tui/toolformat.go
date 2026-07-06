package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// toolResultEntry is the minimal record formatToolSummary needs.
type toolResultEntry struct {
	name    string
	isError bool
	content string
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

var toolFriendlyLabels = map[string]string{
	"bash":  "Running a command",
	"read":  "Reading a file",
	"write": "Writing a file",
	"edit":  "Editing a file",
	"grep":  "Searching in files",
	"find":  "Finding files",
	"ls":    "Listing files",
}

func friendlyToolLabel(name string) string {
	if label, ok := toolFriendlyLabels[name]; ok {
		return label
	}
	return name
}

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

func toolResultBrief(elapsed time.Duration) string {
	if elapsed > 100*time.Millisecond {
		return fmt.Sprintf("%.1fs", elapsed.Seconds())
	}
	return ""
}

// toolPreview returns the first maxLines of content, each truncated to maxWidth,
// followed by a "+N more" hint when there are additional lines.
func toolPreview(content string, maxLines, maxWidth int) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	if len(lines) > maxLines {
		extra := len(lines) - maxLines
		lines = lines[:maxLines]
		lines = append(lines, fmt.Sprintf("… +%d more", extra))
	}
	for i, ln := range lines {
		lines[i] = truncate(ln, maxWidth)
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

// formatExpandedToolResult renders the Ctrl+O expanded view with the complete
// tool arguments and full output. No head/tail truncation is applied so the user
// can inspect everything the tool returned.
func formatExpandedToolResult(toolName, args string, isError bool, content string, elapsed time.Duration, running bool) string {
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

	// Arguments section with pretty-printed JSON when possible.
	if args != "" {
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

// formatToolSummary renders a single collapsed summary line for a turn.
func formatToolSummary(results []toolResultEntry) string {
	total := len(results)
	if total == 0 {
		return ""
	}
	var errCount int
	for _, r := range results {
		if r.isError {
			errCount++
		}
	}
	dimStyle := styleDim()
	okIcon := styleSuccess().Render("✓")
	errIcon := styleError().Render("✗")

	toolWord := "tools"
	if total == 1 {
		toolWord = "tool"
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "⏵ %d %s used  ", total, toolWord)
	if errCount == 0 {
		sb.WriteString(okIcon)
	} else {
		fmt.Fprintf(&sb, "%s %d", okIcon, total-errCount)
		fmt.Fprintf(&sb, "  %s %d", errIcon, errCount)
	}
	return dimStyle.Render(sb.String())
}

func strVal(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

// FormatArgs renders a tool's argument map as a compact JSON snippet for inline
// display. (Relocated from toolpanel.go.)
func FormatArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	data, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	return string(data)
}

// FormatArgsPlain renders a tool's argument map as key=value pairs for human-
// readable prompts (e.g. permission confirmations).
func FormatArgsPlain(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	var parts []string
	for k, v := range args {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return strings.Join(parts, ", ")
}
