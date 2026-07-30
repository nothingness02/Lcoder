package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/tui/components"
)

// Thinking deltas record a start time; commit stamps the duration onto the
// block and the rebuilt component shows the completion label.
func TestThinkingDurationRecordedOnCommit(t *testing.T) {
	m, _, _ := newTestModel()
	m.handleEvent(events.MessageStartEvent{Message: models.AgentMessage{Role: models.RoleAssistant, ID: "s1"}})
	m.handleEvent(events.MessageUpdateEvent{Delta: "reasoning about the bug", IsThinking: true})
	time.Sleep(5 * time.Millisecond)
	m.handleEvent(events.MessageEndEvent{Message: models.AgentMessage{Role: models.RoleAssistant, ID: "s1"}})

	var b *block
	for i := range m.blocks {
		if m.blocks[i].kind == components.BlockAssistant {
			b = &m.blocks[i]
		}
	}
	if b == nil {
		t.Fatal("no assistant block")
	}
	if b.thinkingSecs <= 0 {
		t.Fatalf("thinkingSecs = %v, want > 0", b.thinkingSecs)
	}
	ac := m.components[len(m.blocks)-1].(*components.AssistantComponent)
	out := ac.Render(60, false)
	if !strings.Contains(out, "Thought:") {
		t.Fatalf("expected completion label after commit, got %q", out)
	}
}
