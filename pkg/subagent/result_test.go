package subagent

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
)

func TestParseEventLine(t *testing.T) {
	line := `{"type":"agent_end","reason":"completed","messages":[{"role":"assistant","content":[{"type":"text","text":"hello"}]}]}`
	ev, err := ParseEventLine([]byte(line))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	end, ok := ev.(events.AgentEndEvent)
	if !ok {
		t.Fatalf("expected AgentEndEvent, got %T", ev)
	}
	if end.Reason != events.EndReasonCompleted {
		t.Errorf("reason = %q", end.Reason)
	}
	if len(end.Messages) != 1 || end.Messages[0].Role != models.RoleAssistant {
		t.Errorf("messages = %v", end.Messages)
	}
}

func TestExtractFinalAnswer(t *testing.T) {
	output := []byte(`{"type":"turn_start","turn":1}
{"type":"agent_end","reason":"completed","messages":[{"role":"assistant","content":[{"type":"text","text":"final"}]}]}
`)
	got, err := ExtractFinalAnswer(output)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if got != "final" {
		t.Errorf("got %q, want final", got)
	}
}
