package components

import (
	"strings"
	"testing"
)

func TestAssistantComponentRendersMarkdown(t *testing.T) {
	comp := NewAssistantComponent("a1", "", "# Hello\n\nworld", nil)
	out := comp.Render(40, false)
	if !strings.Contains(out, "Hello") {
		t.Fatalf("missing heading text: %q", out)
	}
	if !strings.Contains(out, "world") {
		t.Fatalf("missing paragraph text: %q", out)
	}
}

func TestAssistantComponentThinking(t *testing.T) {
	comp := NewAssistantComponent("a1", "step one\nstep two", "result", nil)
	out := comp.Render(40, false)
	if !strings.Contains(out, "Thinking:") {
		t.Fatalf("missing thinking preview: %q", out)
	}

	expanded := comp.Render(40, true)
	if !strings.Contains(expanded, "step one") || !strings.Contains(expanded, "step two") {
		t.Fatalf("missing expanded thinking lines: %q", expanded)
	}
}

func TestAssistantComponentUsage(t *testing.T) {
	comp := NewAssistantComponent("a1", "", "hi", &UsageInfo{TotalTokens: 42, Cost: 0.0012})
	out := comp.Render(40, false)
	if !strings.Contains(out, "42 tokens") {
		t.Fatalf("missing usage tokens: %q", out)
	}
	if !strings.Contains(out, "$0.0012") {
		t.Fatalf("missing usage cost: %q", out)
	}
}

func TestAssistantComponentSetContent(t *testing.T) {
	comp := NewAssistantComponent("a1", "", "first", nil)
	comp.SetContent("second")
	out := comp.Render(40, false)
	if !strings.Contains(out, "second") {
		t.Fatalf("expected updated content: %q", out)
	}
	if strings.Contains(out, "first") {
		t.Fatalf("old content should be gone: %q", out)
	}
}

func TestAssistantComponentUpdateToggleExpanded(t *testing.T) {
	comp := NewAssistantComponent("a1", "thinking", "content", nil)
	if comp.expanded {
		t.Fatal("expected collapsed by default")
	}
	updated, _ := comp.Update(ToggleExpandedMsg{})
	ac := updated.(*AssistantComponent)
	if !ac.expanded {
		t.Fatal("expected expanded after toggle")
	}
}
