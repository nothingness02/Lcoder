// pkg/tui/markdown/highlight.go
package markdown

import (
	"bytes"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	chromaStyles "github.com/alecthomas/chroma/v2/styles"
)

// HighlightCodeLines renders source code as one ANSI-styled string per source
// line, picking the lexer from the file path's extension. Falls back to
// unstyled lines when no lexer matches or highlighting fails, so callers can
// rely on the result having exactly one entry per source line.
func HighlightCodeLines(source, path string) []string {
	plain := strings.Split(source, "\n")

	lexer := lexers.Match(path)
	if lexer == nil {
		return plain
	}
	lexer = chroma.Coalesce(lexer)

	styleName := "github"
	if isDarkBackground() {
		styleName = "monokai"
	}
	style := chromaStyles.Get(styleName)

	formatter := formatters.Get("terminal16m")
	if formatter == nil {
		return plain
	}

	iterator, err := lexer.Tokenise(nil, source)
	if err != nil {
		return plain
	}
	var buf bytes.Buffer
	if err := formatter.Format(&buf, style, iterator); err != nil {
		return plain
	}
	out := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(out) != len(plain) {
		// A formatter that does not preserve line count would misalign the
		// gutter numbers; plain lines are the safe fallback.
		return plain
	}
	return out
}
