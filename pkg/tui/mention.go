package tui

import (
	"os"
	"path/filepath"
	"strings"
)

// parseMentions extracts @<path> tokens from text. A mention starts at '@'
// preceded by start-of-text or whitespace; the path runs until the next space.
func parseMentions(text string) []string {
	var out []string
	for i := 0; i < len(text); i++ {
		if text[i] != '@' {
			continue
		}
		if i > 0 && !isMentionSpace(text[i-1]) {
			continue
		}
		j := i + 1
		for j < len(text) && !isMentionSpace(text[j]) {
			j++
		}
		if j > i+1 {
			out = append(out, text[i+1:j])
		}
		i = j
	}
	return out
}

func isMentionSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' }

// resolveMention resolves raw (cwd-relative, absolute, or ~-prefixed) to an
// absolute path and reports whether it exists. Directories count as valid
// mentions: the agent's ls tool can enumerate them.
func resolveMention(cwd, raw string) (string, bool) {
	p := expandHome(raw)
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	p = filepath.Clean(p)
	if _, err := os.Stat(p); err != nil {
		return p, false
	}
	return p, true
}

// expandHome rewrites a leading ~ to the user's home directory.
func expandHome(raw string) string {
	if raw != "~" && !strings.HasPrefix(raw, "~/") {
		return raw
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return raw
	}
	return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(raw, "~"), "/"))
}

// validateMentions returns the raw mentions that do not resolve to an
// existing file or directory.
func validateMentions(cwd, text string) []string {
	var missing []string
	for _, raw := range parseMentions(text) {
		if _, ok := resolveMention(cwd, raw); !ok {
			missing = append(missing, raw)
		}
	}
	return missing
}

// mentionLabels returns display basenames for the resolvable mentions in text.
func mentionLabels(cwd, text string) []string {
	var out []string
	for _, raw := range parseMentions(text) {
		if abs, ok := resolveMention(cwd, raw); ok {
			out = append(out, filepath.Base(abs))
		}
	}
	return out
}

// expandHomeMentions rewrites @~/... mentions to absolute paths so the agent's
// read tool (which cannot expand ~) can resolve them. Other mentions are kept
// verbatim.
func expandHomeMentions(text string) string {
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		if text[i] == '@' && (i == 0 || isMentionSpace(text[i-1])) {
			j := i + 1
			for j < len(text) && !isMentionSpace(text[j]) {
				j++
			}
			raw := text[i+1 : j]
			if raw == "~" || strings.HasPrefix(raw, "~/") {
				b.WriteString("@")
				b.WriteString(filepath.ToSlash(expandHome(raw)))
				i = j - 1
				continue
			}
		}
		b.WriteByte(text[i])
	}
	return b.String()
}
