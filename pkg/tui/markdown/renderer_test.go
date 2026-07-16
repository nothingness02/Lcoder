package markdown

import (
	"strings"
	"testing"
)

func TestRenderMarkdownRendersHeading(t *testing.T) {
	out := RenderMarkdown("# Hello", 80)
	if !strings.Contains(out, "Hello") {
		t.Fatalf("expected rendered heading to contain Hello, got %q", out)
	}
}

func TestRenderMarkdownRendersInlineCode(t *testing.T) {
	out := RenderMarkdown("use `foo()`", 80)
	if !strings.Contains(out, "foo()") {
		t.Fatalf("expected inline code to survive, got %q", out)
	}
}

func TestRenderMarkdownRendersList(t *testing.T) {
	out := RenderMarkdown("- one\n- two", 80)
	if !strings.Contains(out, "one") || !strings.Contains(out, "two") {
		t.Fatalf("expected list items, got %q", out)
	}
}

func TestRenderMarkdownEmpty(t *testing.T) {
	out := RenderMarkdown("", 80)
	if out != "" {
		t.Fatalf("expected empty output, got %q", out)
	}
}

func TestPreprocessMathDisplay(t *testing.T) {
	src := "answer is $$x = 1$$ ok"
	out := preprocessMath(src)
	if !strings.Contains(out, "```math") {
		t.Fatalf("expected display math fenced block, got %q", out)
	}
	if !strings.Contains(out, "x = 1") {
		t.Fatalf("expected formula preserved, got %q", out)
	}
}

func TestPreprocessMathInline(t *testing.T) {
	src := "let $x = 1$ now"
	out := preprocessMath(src)
	if strings.Contains(out, "$x = 1$") {
		t.Fatalf("inline math markers should be replaced, got %q", out)
	}
	if !strings.Contains(out, "x = 1") {
		t.Fatalf("expected formula preserved, got %q", out)
	}
}

func TestPreprocessMathEscapedDollar(t *testing.T) {
	src := "cost is \\$5"
	out := preprocessMath(src)
	if strings.Contains(out, "```math") {
		t.Fatalf("escaped dollar should not start math block, got %q", out)
	}
}

func TestRenderMarkdownMathSurvives(t *testing.T) {
	out := RenderMarkdown("$$\\sum_{i=1}^{n} i$$", 80)
	if !strings.Contains(out, "\\sum") {
		t.Fatalf("expected math source to survive rendering, got %q", out)
	}
}
