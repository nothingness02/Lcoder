package tui

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/tui/components"
)

// Tool-call argument deltas must not accumulate into the visible stream.
func TestToolCallDeltaNotRenderedAsText(t *testing.T) {
	m, _, _ := newTestModel()
	m.handleEvent(events.MessageStartEvent{Message: models.AgentMessage{Role: models.RoleAssistant, ID: "s1"}})
	m.handleEvent(events.MessageUpdateEvent{Delta: `{"path":"pkg/extension/loader.go"}`, IsToolCall: true})

	if m.streamLive != "" {
		t.Fatalf("tool-call JSON leaked into streamLive: %q", m.streamLive)
	}
	if m.blocks[0].raw != "" {
		t.Fatalf("tool-call JSON leaked into block raw: %q", m.blocks[0].raw)
	}
}

// A pure tool-call turn has no assistant text; the commit fallback
// (final == "" → streamLive) must not resurrect the JSON args.
func TestPureToolCallTurnLeavesNoJSON(t *testing.T) {
	m, _, _ := newTestModel()
	m.handleEvent(events.MessageStartEvent{Message: models.AgentMessage{Role: models.RoleAssistant, ID: "s1"}})
	m.handleEvent(events.MessageUpdateEvent{Delta: `{"path":"x.go"}`, IsToolCall: true})

	final := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Type: "tool_call", ID: "c1", Name: "read", Arguments: map[string]any{"path": "x.go"},
	})
	final.ID = "s1"
	m.handleEvent(events.MessageEndEvent{Message: final})

	for _, b := range m.blocks {
		if b.kind == components.BlockAssistant && strings.Contains(b.raw, `"path"`) {
			t.Fatalf("committed assistant block contains tool-call JSON: %q", b.raw)
		}
	}
}
