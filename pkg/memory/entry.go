package memory

import (
	"fmt"
	"strings"
)

// charCount returns the sum of entry text lengths (separators not counted).
func charCount(entries []string) int {
	n := 0
	for _, e := range entries {
		n += len(e)
	}
	return n
}

// parseEntries reads a memory file body and returns its entries.
// It ignores decorative header lines and splits on the § separator.
func parseEntries(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	parts := strings.Split(text, "\n"+EntrySeparator+"\n")
	var entries []string
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if i == 0 {
			p = stripHeader(p)
		}
		if p != "" {
			entries = append(entries, p)
		}
	}
	return entries
}

// stripHeader drops the decorative header lines (═══... and title/usage).
func stripHeader(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	skipped := false
	for _, l := range lines {
		if !skipped {
			trim := strings.TrimSpace(l)
			if trim == "" || strings.HasPrefix(trim, "═") ||
				strings.HasPrefix(trim, "MEMORY") || strings.HasPrefix(trim, "USER") {
				continue
			}
			skipped = true
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// joinEntries serializes entries with the § separator.
func joinEntries(entries []string) string {
	if len(entries) == 0 {
		return ""
	}
	return strings.Join(entries, "\n"+EntrySeparator+"\n")
}

// formatFile produces the on-disk representation including header.
func formatFile(title string, entries []string, limit int) string {
	usage := charCount(entries)
	pct := 0
	if limit > 0 {
		pct = usage * 100 / limit
	}
	header := fmt.Sprintf("═══════════════════════════════════════\n%s [%d%% — %d/%d chars]\n═══════════════════════════════════════\n", title, pct, usage, limit)
	body := joinEntries(entries)
	if body == "" {
		return header
	}
	return header + body
}

// findEntryIndex returns the unique entry index containing substring.
func findEntryIndex(entries []string, substring string) (int, error) {
	if substring == "" {
		return -1, fmt.Errorf("old_text cannot be empty")
	}
	var matches []int
	for i, e := range entries {
		if strings.Contains(e, substring) {
			matches = append(matches, i)
		}
	}
	switch len(matches) {
	case 0:
		return -1, fmt.Errorf("old_text %q did not match any entry", substring)
	case 1:
		return matches[0], nil
	default:
		return -1, fmt.Errorf("old_text %q matched %d entries; provide a more specific substring", substring, len(matches))
	}
}
