package tui

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/tui/components"
)

func TestRenderUserBlock(t *testing.T) {
	b := block{kind: components.BlockUser, raw: "hello world"}
	out := b.render(80, false)
	if !strings.Contains(out, "hello world") {
		t.Fatalf("user block missing text: %q", out)
	}
}

func TestRenderAssistantBlockMarkdown(t *testing.T) {
	b := block{kind: components.BlockAssistant, raw: "# Hi\n\ntext"}
	out := b.render(80, false)
	if strings.Contains(out, "# Hi") {
		t.Fatal("assistant block did not render markdown")
	}
}

func TestRenderAssistantThinkingCompactVsExpanded(t *testing.T) {
	thinking := "line one\nline two\nline three with a lot of extra detail that only matters when fully expanded"
	b := block{kind: components.BlockAssistant, raw: "answer", thinking: thinking}
	compact := b.render(80, false)
	expanded := b.render(80, true)
	if compact == expanded {
		t.Fatal("expanded thinking should differ from compact")
	}
	// Compact preview collapses newlines into a single line.
	if strings.Contains(compact, "line one\nline two") {
		t.Fatalf("compact thinking should collapse newlines: %q", compact)
	}
	// Expanded view preserves the full multi-line trace.
	if !strings.Contains(expanded, "line two") || !strings.Contains(expanded, "line three") {
		t.Fatalf("expanded thinking should show full trace: %q", expanded)
	}
}

func TestRenderSystemBlock(t *testing.T) {
	b := block{kind: components.BlockSystem, raw: "switched mode"}
	if out := b.render(80, false); !strings.Contains(out, "switched mode") {
		t.Fatalf("system block missing text: %q", out)
	}
}
