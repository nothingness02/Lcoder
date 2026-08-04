// pkg/tui/markdown/highlight_test.go
package markdown

import (
	"strings"
	"testing"
)

func TestHighlightCodeLinesPreservesLineCount(t *testing.T) {
	source := "package main\n\n// comment\nfunc main() {}\n"
	lines := HighlightCodeLines(strings.TrimSuffix(source, "\n"), "main.go")
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines, got %d: %q", len(lines), lines)
	}
	// Known extension gets ANSI styling.
	if lines[0] == "package main" {
		t.Fatalf("expected styled output for .go file, got %q", lines[0])
	}
}

func TestHighlightCodeLinesFallback(t *testing.T) {
	// Unknown extension: plain lines back, one per source line.
	lines := HighlightCodeLines("a\nb", "file.zzzunknown")
	if len(lines) != 2 || lines[0] != "a" || lines[1] != "b" {
		t.Fatalf("expected plain fallback, got %q", lines)
	}
}
