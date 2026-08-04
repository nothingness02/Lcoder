package components

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lcoder/lcoder/pkg/tui/markdown"
)

// oldContentDetailKey is the ToolExecutionResult.Details key under which the
// write tool ships the previous file content on overwrite (see
// pkg/tools/builtin/write.go), enabling a collapsed diff preview.
const oldContentDetailKey = "old_content"

// compactPreviewMaxLines caps the collapsed edit/write diff and the write
// content preview shown under a tool header.
const compactPreviewMaxLines = 10

// editArgPair is one oldText/newText replacement from an edit tool call.
type editArgPair struct{ oldText, newText string }

// parseEditArgPairs extracts the edits from an edit tool call's JSON
// arguments. Returns nil when the args cannot be parsed into edits.
func parseEditArgPairs(argsJSON string) []editArgPair {
	var m map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &m); err != nil {
		return nil
	}
	edits, ok := m["edits"].([]any)
	if !ok || len(edits) == 0 {
		return nil
	}
	pairs := make([]editArgPair, 0, len(edits))
	for _, raw := range edits {
		edit, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		oldText, _ := edit["oldText"].(string)
		newText, _ := edit["newText"].(string)
		pairs = append(pairs, editArgPair{oldText: oldText, newText: newText})
	}
	return pairs
}

// EditDiffStats sums the added/removed line counts across an edit call's
// edits (kimi-code's edit header chip).
func EditDiffStats(argsJSON string) (added, removed int) {
	for _, p := range parseEditArgPairs(argsJSON) {
		a, r := DiffStats(p.oldText, p.newText)
		added += a
		removed += r
	}
	return added, removed
}

// EditDiffRows renders every edit in an edit tool call as clustered diff
// rows, sharing the maxLines budget across edits (maxLines<=0: no cap).
func EditDiffRows(argsJSON string, maxLines int, expandHint string) []string {
	pairs := parseEditArgPairs(argsJSON)
	if len(pairs) == 0 {
		return nil
	}
	var out []string
	hidden := 0
	for _, p := range pairs {
		remaining := 0
		if maxLines > 0 {
			remaining = maxLines - len(out)
			if remaining <= 0 {
				a, r := DiffStats(p.oldText, p.newText)
				hidden += a + r
				continue
			}
		}
		rows, h := renderClusteredRows(computeDiffLines(p.oldText, p.newText), 3, remaining)
		out = append(out, rows...)
		hidden += h
	}
	if footer := hiddenChangesFooter(hidden, expandHint); footer != "" {
		out = append(out, footer)
	}
	return out
}

// WriteContentFromArgs extracts the content argument of a write tool call.
func WriteContentFromArgs(argsJSON string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &m); err != nil {
		return ""
	}
	content, _ := m["content"].(string)
	return content
}

// writePathFromArgs extracts the path argument of a write tool call (used to
// pick the syntax-highlighting lexer).
func writePathFromArgs(argsJSON string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &m); err != nil {
		return ""
	}
	path, _ := m["path"].(string)
	return path
}

// WriteContentRows renders file content as syntax-highlighted rows with a dim
// 4-wide line-number gutter, capped at maxLines (<=0: no cap) with a
// `… +N more` hint when truncated.
func WriteContentRows(content, path string, maxLines int, expandHint string) []string {
	content = strings.TrimSuffix(content, "\n")
	if content == "" {
		return nil
	}
	lines := markdown.HighlightCodeLines(content, path)
	total := len(lines)
	shown := lines
	if maxLines > 0 && total > maxLines {
		shown = lines[:maxLines]
	}
	rows := make([]string, 0, len(shown)+1)
	for i, ln := range shown {
		rows = append(rows, styleDim().Render(fmt.Sprintf("%4d  ", i+1))+ln)
	}
	if len(shown) < total {
		hint := fmt.Sprintf("     … +%d more", total-len(shown))
		if expandHint != "" {
			hint += fmt.Sprintf(" (%s to expand)", expandHint)
		}
		rows = append(rows, styleDim().Render(hint))
	}
	return rows
}

// compactToolPreview builds the collapsed body shown under a finished tool
// header. read renders no body at all; edit renders a clustered diff of its
// edits; write renders a diff on overwrite (old content from result details)
// or a highlighted content preview for new files; bash shows the output tail.
// Errors always fall back to the generic head/tail sample so the error text
// stays visible.
func compactToolPreview(toolName, argsJSON string, details map[string]any, result string, isError bool, width int) string {
	if isError {
		return collapseToolOutput(result, 2, 1, width)
	}
	switch toolName {
	case "read":
		return ""
	case "bash":
		return collapseToolOutputTail(result, 3, width)
	case "edit":
		if rows := EditDiffRows(argsJSON, compactPreviewMaxLines, diffExpandHint); len(rows) > 0 {
			return strings.Join(rows, "\n")
		}
	case "write":
		if old, ok := details[oldContentDetailKey].(string); ok && old != "" {
			return strings.Join(RenderClusteredDiff(old, WriteContentFromArgs(argsJSON), compactPreviewMaxLines, diffExpandHint), "\n")
		}
		if rows := WriteContentRows(WriteContentFromArgs(argsJSON), writePathFromArgs(argsJSON), compactPreviewMaxLines, diffExpandHint); len(rows) > 0 {
			return strings.Join(rows, "\n")
		}
	}
	return collapseToolOutput(result, 2, 1, width)
}

// collapseToolOutputTail returns the last tail lines of content with an
// elision marker on top when content is longer, each line clipped to width.
// Tailing suits logs and shell output, where the salient part is at the end.
func collapseToolOutputTail(content string, tail, maxWidth int) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	if len(lines) > tail {
		omitted := len(lines) - tail
		sampled := make([]string, 0, tail+1)
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
