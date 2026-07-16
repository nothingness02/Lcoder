// pkg/tui/markdown/renderer.go
package markdown

import (
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
)

// RenderMarkdown renders markdown text to an ANSI string using glamour.
// Inline and display math ($...$ / $$...$$) are converted to code blocks/spans
// before rendering so the formulas are preserved and styled in the terminal.
func RenderMarkdown(source string, width int) string {
	if source == "" {
		return ""
	}
	source = preprocessMath(source)
	r := getMarkdownRenderer(width)
	if r == nil {
		return source
	}
	out, err := r.Render(source)
	if err != nil {
		return source
	}
	return strings.TrimRight(out, "\n ")
}

var (
	mdRendererCache   = map[int]*glamour.TermRenderer{}
	mdRendererCacheMu sync.RWMutex
)

func getMarkdownRenderer(width int) *glamour.TermRenderer {
	mdRendererCacheMu.RLock()
	if r, ok := mdRendererCache[width]; ok {
		mdRendererCacheMu.RUnlock()
		return r
	}
	mdRendererCacheMu.RUnlock()

	mdRendererCacheMu.Lock()
	defer mdRendererCacheMu.Unlock()
	if r, ok := mdRendererCache[width]; ok {
		return r
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return nil
	}
	mdRendererCache[width] = r
	return r
}

// preprocessMath converts LaTeX-style math markers into glamour-friendly code
// blocks/spans so the formulas survive markdown rendering. Display math
// ($$...$$) becomes a fenced code block; inline math ($...$) becomes inline
// code. Escaped dollar signs (\$) are left alone.
func preprocessMath(source string) string {
	var sb strings.Builder
	for i := 0; i < len(source); {
		if source[i] == '$' && !isEscaped(source, i) {
			if i+1 < len(source) && source[i+1] == '$' {
				if end := findUnescaped(source, i+2, "$$"); end != -1 {
					sb.WriteString("\n\n```math\n")
					sb.WriteString(source[i+2 : end])
					sb.WriteString("\n```\n\n")
					i = end + 2
					continue
				}
			}
			if end := findUnescaped(source, i+1, "$"); end != -1 {
				sb.WriteString(mathInlineCode(source[i+1 : end]))
				i = end + 1
				continue
			}
		}
		sb.WriteByte(source[i])
		i++
	}
	return sb.String()
}

// isEscaped reports whether the byte at idx is preceded by an odd number of
// backslashes.
func isEscaped(source string, idx int) bool {
	if idx == 0 {
		return false
	}
	backslashes := 0
	for j := idx - 1; j >= 0 && source[j] == '\\'; j-- {
		backslashes++
	}
	return backslashes%2 == 1
}

// findUnescaped returns the index of the first occurrence of delim after start
// that is not escaped, or -1 if none is found.
func findUnescaped(source string, start int, delim string) int {
	for i := start; i <= len(source)-len(delim); i++ {
		if source[i:i+len(delim)] == delim && !isEscaped(source, i) {
			return i
		}
	}
	return -1
}

// mathInlineCode wraps math source in backticks, choosing enough backticks to
// avoid clashing with backticks inside the formula.
func mathInlineCode(src string) string {
	max := 0
	cur := 0
	for i := 0; i < len(src); i++ {
		if src[i] == '`' {
			cur++
			if cur > max {
				max = cur
			}
		} else {
			cur = 0
		}
	}
	ticks := strings.Repeat("`", max+1)
	return ticks + src + ticks
}
