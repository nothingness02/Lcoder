package components

import (
	"strings"
	"testing"
)

func TestAssistantThoughtDurationCollapsed(t *testing.T) {
	c := NewAssistantComponent("a1", "reasoning step one\nstep two", "answer", nil)
	c.SetThinkingSecs(12.3)
	out := c.Render(60, false)
	if !strings.Contains(out, "Thought:") {
		t.Fatalf("collapsed completed thinking should read Thought:, got %q", out)
	}
	if !strings.Contains(out, "12s") {
		t.Fatalf("expected duration in label, got %q", out)
	}
	if !strings.Contains(out, "reasoning step one") {
		t.Fatalf("expected first-line summary, got %q", out)
	}
}

func TestAssistantThinkingStreamingLabel(t *testing.T) {
	c := NewAssistantComponent("a1", "reasoning", "", nil)
	out := c.Render(60, false)
	if !strings.Contains(out, "Thinking:") {
		t.Fatalf("streaming thinking should read Thinking:, got %q", out)
	}
	if strings.Contains(out, "Thought:") {
		t.Fatalf("no completion label while streaming, got %q", out)
	}
}

func TestAssistantThoughtSubTenSecondPrecision(t *testing.T) {
	c := NewAssistantComponent("a1", "reasoning", "answer", nil)
	c.SetThinkingSecs(3.25)
	out := c.Render(60, false)
	if !strings.Contains(out, "3.2s") && !strings.Contains(out, "3.3s") {
		t.Fatalf("expected one-decimal duration, got %q", out)
	}
}
