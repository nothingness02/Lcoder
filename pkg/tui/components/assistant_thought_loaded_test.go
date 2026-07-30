package components

import (
	"strings"
	"testing"
)

// A loaded-from-session trace is complete but has no recorded duration: it
// shows the completion label without the seconds segment.
func TestAssistantThoughtWithoutDuration(t *testing.T) {
	c := NewAssistantComponent("a1", "old reasoning", "answer", nil)
	c.SetThinkingSecs(-1)
	out := c.Render(60, false)
	if !strings.Contains(out, "Thought:") {
		t.Fatalf("expected completion label, got %q", out)
	}
	if strings.Contains(out, "·") || strings.Contains(out, "0.0s") {
		t.Fatalf("no duration segment expected, got %q", out)
	}
	if strings.Contains(out, "Thinking:") {
		t.Fatalf("must not look like an in-flight trace, got %q", out)
	}
}
