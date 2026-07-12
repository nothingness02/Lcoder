package tui

import (
	"strings"
	"testing"
)

func TestRenderMarkdownBasic(t *testing.T) {
	out := renderMarkdown("# Title\n\nsome **bold** text", 80)
	if out == "" {
		t.Fatal("empty render")
	}
	if strings.Contains(out, "# Title") {
		t.Fatal("heading markdown not transformed")
	}
}

func TestRenderMarkdownCacheHit(t *testing.T) {
	a := renderMarkdownCached("hello `code`", 80)
	b := renderMarkdownCached("hello `code`", 80)
	if a != b {
		t.Fatal("cache returned different output for same input")
	}
}

func TestRenderMarkdownEmpty(t *testing.T) {
	if out := renderMarkdown("", 80); out != "" {
		t.Fatalf("empty input render = %q, want empty", out)
	}
}

func TestRenderMarkdownEscapesNewlines(t *testing.T) {
	out := renderMarkdown("line one\\nline two\\nline three", 80)
	if !strings.Contains(out, "line one") || !strings.Contains(out, "line two") {
		t.Fatalf("expected escaped newlines to render as line breaks, got %q", out)
	}
	// After unescaping, the three lines should no longer contain the literal \n sequence.
	if strings.Contains(out, "\\n") {
		t.Fatalf("output still contains literal \\n: %q", out)
	}
}

func TestRenderMarkdownEscapesTabs(t *testing.T) {
	out := renderMarkdown("col1\\tcol2", 80)
	if strings.Contains(out, "\\t") {
		t.Fatalf("output still contains literal \\t: %q", out)
	}
}
