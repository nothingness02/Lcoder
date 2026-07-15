package tui

import (
	"encoding/json"
	"fmt"
	"strings"
)

// toolResultEntry is the minimal record formatToolSummary needs.
type toolResultEntry struct {
	name    string
	isError bool
	content string
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
