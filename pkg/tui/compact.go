package tui

import (
	"fmt"
	"strconv"
	"strings"
)

// compactSummaryMaxRunes caps the fold summary echoed into the conversation, so
// a long summary doesn't flood the transcript.
const compactSummaryMaxRunes = 200

// formatCompactResult renders the compaction notice: a before/after token
// comparison plus the fold summary (capped at compactSummaryMaxRunes), all dim.
func formatCompactResult(before, after int, summary string) string {
	head := fmt.Sprintf("↧ 已压缩早前对话:~%s → ~%s token", formatTokenCount(before), formatTokenCount(after))
	s := strings.TrimSpace(summary)
	if s == "" {
		return styleDim().Render(head)
	}
	if r := []rune(s); len(r) > compactSummaryMaxRunes {
		s = string(r[:compactSummaryMaxRunes]) + "…"
	}
	return styleDim().Render(head + "\n" + s)
}

// formatTokenCount renders an integer with thousands separators (12,345).
func formatTokenCount(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	s := strconv.Itoa(n)
	var sb strings.Builder
	lead := len(s) % 3
	if lead > 0 {
		sb.WriteString(s[:lead])
	}
	for i := lead; i < len(s); i += 3 {
		if sb.Len() > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(s[i : i+3])
	}
	return sb.String()
}
