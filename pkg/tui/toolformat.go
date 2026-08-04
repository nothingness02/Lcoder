package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/tui/components"
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

// chipForTool computes a compact result statistic for the tool header
// (kimi-code's header chip): line counts for file/shell tools, match counts
// for search tools, a +added/-removed diffstat for edit. Empty when nothing
// meaningful. argsJSON is the tool call's serialized arguments (may be empty
// for late-appended blocks, in which case chips fall back to result data).
func chipForTool(name, argsJSON string, result models.ToolExecutionResult) string {
	text := result.Text()
	lines := 0
	for _, ln := range strings.Split(text, "\n") {
		if strings.TrimSpace(ln) != "" {
			lines++
		}
	}
	detail := func(key string) (int, bool) {
		if result.Details == nil {
			return 0, false
		}
		v, ok := result.Details[key].(int)
		return v, ok
	}
	switch name {
	case "bash", "read":
		return countLabel(lines, "lines")
	case "write":
		if content := components.WriteContentFromArgs(argsJSON); content != "" {
			content = strings.TrimSuffix(content, "\n")
			return countLabel(len(strings.Split(content, "\n")), "lines")
		}
		return countLabel(lines, "lines")
	case "ls":
		return countLabel(lines, "entries")
	case "grep":
		if v, ok := detail("matches"); ok {
			return countLabel(v, "matches")
		}
		return countLabel(lines, "lines")
	case "find":
		if v, ok := detail("matches"); ok {
			return countLabel(v, "files")
		}
		return countLabel(lines, "files")
	case "edit":
		if added, removed := components.EditDiffStats(argsJSON); added > 0 || removed > 0 {
			var parts []string
			if added > 0 {
				parts = append(parts, fmt.Sprintf("+%d", added))
			}
			if removed > 0 {
				parts = append(parts, fmt.Sprintf("-%d", removed))
			}
			return strings.Join(parts, " ")
		}
		if v, ok := detail("edits"); ok {
			return countLabel(v, "edits")
		}
	}
	return ""
}

func countLabel(n int, unit string) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("%d %s", n, unit)
}
