package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/tui/components"
)

func TestErrorEventShowsInFixedRegionNotTranscript(t *testing.T) {
	m, _, _ := newTestModel()
	m.mainWidth = 80
	before := len(m.blocks)
	m.handleEvent(events.ErrorEvent{Base: events.Base{Type: events.Error, Turn: 1}, Message: "boom"})
	if len(m.blocks) != before {
		t.Fatalf("error must not append a transcript block: %d -> %d", before, len(m.blocks))
	}
	if m.errMsg != "boom" {
		t.Fatalf("errMsg = %q, want boom", m.errMsg)
	}
	if !strings.Contains(stripANSI(m.bottomRegion()), "boom") {
		t.Fatal("error should render in the fixed bottom region")
	}
}

func TestAgentDoneErrorGoesToFixedRegion(t *testing.T) {
	m, _, _ := newTestModel()
	m.mainWidth = 80
	before := len(m.blocks)
	m.onAgentDone(errors.New("run failed"))
	if len(m.blocks) != before {
		t.Fatalf("run error must not append a transcript block: %d -> %d", before, len(m.blocks))
	}
	if m.errMsg != "run failed" {
		t.Fatalf("errMsg = %q, want run failed", m.errMsg)
	}
}

func TestCompactionIndicatorLifecycle(t *testing.T) {
	m, _, _ := newTestModel()

	m.handleEvent(events.CompactionStartedEvent{})
	if !m.compacting {
		t.Fatal("CompactionStartedEvent must set compacting")
	}

	m.state = stateProcessing
	m.mainWidth = 80
	if view := m.statusLineView(); !strings.Contains(view, "压缩中") {
		t.Fatalf("status line must show compacting indicator, got %q", view)
	}

	m.handleEvent(events.CompactionCommittedEvent{})
	if m.compacting {
		t.Fatal("CompactionCommittedEvent must clear compacting")
	}
	if view := m.statusLineView(); strings.Contains(view, "压缩中") {
		t.Fatalf("indicator must be gone after commit, got %q", view)
	}
}

func TestTurnEndEventAddsToolSummaryBelowToolCalls(t *testing.T) {
	m, _, _ := newTestModel()

	m.handleEvent(events.ToolExecutionStartEvent{
		ToolCallID: "c1",
		ToolName:   "bash",
		Args:       map[string]any{"command": "ls"},
	})
	m.handleEvent(events.ToolExecutionEndEvent{
		ToolCallID: "c1",
		ToolName:   "bash",
		Result:     models.NewToolExecutionResultText("file1\nfile2"),
		IsError:    false,
	})

	toolCount := len(m.blocks)
	if toolCount == 0 {
		t.Fatal("expected a tool block after tool events")
	}

	m.handleEvent(events.TurnEndEvent{
		Message:     models.AgentMessage{Role: models.RoleAssistant},
		ToolResults: []models.AgentMessage{models.NewAgentMessage(models.RoleToolResult, models.TextContent{Text: "file1\nfile2"})},
	})

	if len(m.blocks) != toolCount+1 {
		t.Fatalf("expected summary block appended right after tools, got %d blocks (want %d)", len(m.blocks), toolCount+1)
	}
	last := m.blocks[len(m.blocks)-1]
	if last.kind != components.BlockSystem {
		t.Fatalf("expected system summary block, got %v", last.kind)
	}
	if !strings.Contains(last.raw, "1 tool") {
		t.Fatalf("expected summary text to mention tool count, got %q", last.raw)
	}
	if len(m.turnTools) != 0 {
		t.Fatalf("turnTools should be reset after summary, got %d", len(m.turnTools))
	}
}

func TestAgentDoneDoesNotDuplicateToolSummary(t *testing.T) {
	m, _, _ := newTestModel()

	m.handleEvent(events.ToolExecutionStartEvent{
		ToolCallID: "c1",
		ToolName:   "bash",
		Args:       map[string]any{"command": "ls"},
	})
	m.handleEvent(events.ToolExecutionEndEvent{
		ToolCallID: "c1",
		ToolName:   "bash",
		Result:     models.NewToolExecutionResultText("file1"),
		IsError:    false,
	})
	m.handleEvent(events.TurnEndEvent{
		Message:     models.AgentMessage{Role: models.RoleAssistant},
		ToolResults: []models.AgentMessage{models.NewAgentMessage(models.RoleToolResult, models.TextContent{Text: "file1"})},
	})

	afterTurnEnd := len(m.blocks)
	m.onAgentDone(nil)
	if len(m.blocks) != afterTurnEnd {
		t.Fatalf("onAgentDone should not append another summary, blocks changed from %d to %d", afterTurnEnd, len(m.blocks))
	}
}

func TestMessageEndUsesStreamingBlockID(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateProcessing

	m.handleEvent(events.MessageStartEvent{Message: models.AgentMessage{Role: models.RoleAssistant, ID: "stream-id"}})
	m.handleEvent(events.MessageUpdateEvent{Delta: "hello"})

	// Provider may finalize with a different message object/ID.
	final := models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "hello world"})
	final.ID = "final-id"
	m.handleEvent(events.MessageEndEvent{Message: final})

	var assistantBlocks int
	var lastRaw string
	for _, b := range m.blocks {
		if b.kind == components.BlockAssistant {
			assistantBlocks++
			lastRaw = b.raw
		}
	}
	if assistantBlocks != 1 {
		t.Fatalf("expected 1 assistant block, got %d", assistantBlocks)
	}
	if lastRaw != "hello world" {
		t.Fatalf("expected 'hello world', got %q", lastRaw)
	}
}

func TestThinkingDeltaDoesNotLeakIntoContent(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateProcessing

	m.handleEvent(events.MessageStartEvent{Message: models.AgentMessage{Role: models.RoleAssistant, ID: "a1"}})
	m.handleEvent(events.MessageUpdateEvent{Delta: "reasoning ", IsThinking: true})
	m.handleEvent(events.MessageUpdateEvent{Delta: "step", IsThinking: true})
	m.handleEvent(events.MessageUpdateEvent{Delta: "answer"})

	final := models.NewAgentMessage(models.RoleAssistant,
		models.ThinkingContent{Text: "reasoning step"},
		models.TextContent{Text: "answer"},
	)
	final.ID = "a1"
	m.handleEvent(events.MessageEndEvent{Message: final})

	for _, b := range m.blocks {
		if b.kind != components.BlockAssistant {
			continue
		}
		if b.raw != "answer" {
			t.Fatalf("content should be answer only, got %q", b.raw)
		}
		if b.thinking != "reasoning step" {
			t.Fatalf("thinking should be reasoning step, got %q", b.thinking)
		}
		ac, ok := m.components[0].(*components.AssistantComponent)
		if !ok {
			t.Fatalf("expected assistant component, got %T", m.components[0])
		}
		rendered := ac.Render(40, false)
		// Collapsed thinking renders as a one-line summary ("Thinking: <first
		// line>"). The reasoning trace must stay out of the content area, which
		// the b.raw assertion above already pins to "answer".
		if !strings.Contains(rendered, "Thinking: reasoning step") {
			t.Fatalf("collapsed thinking should show first-line summary, got %q", rendered)
		}
	}
}

func TestToolSummaryAppearsBeforeNextAssistantMessage(t *testing.T) {
	m, _, _ := newTestModel()

	m.handleEvent(events.ToolExecutionStartEvent{
		ToolCallID: "c1",
		ToolName:   "bash",
		Args:       map[string]any{"command": "ls"},
	})
	m.handleEvent(events.ToolExecutionEndEvent{
		ToolCallID: "c1",
		ToolName:   "bash",
		Result:     models.NewToolExecutionResultText("file1"),
		IsError:    false,
	})
	m.handleEvent(events.TurnEndEvent{
		Message:     models.AgentMessage{Role: models.RoleAssistant},
		ToolResults: []models.AgentMessage{models.NewAgentMessage(models.RoleToolResult, models.TextContent{Text: "file1"})},
	})
	m.handleEvent(events.MessageStartEvent{Message: models.AgentMessage{Role: models.RoleAssistant, ID: "a2"}})

	if len(m.blocks) < 3 {
		t.Fatalf("expected tool + summary + assistant, got %d blocks", len(m.blocks))
	}
	summaryIdx := len(m.blocks) - 2
	assistantIdx := len(m.blocks) - 1
	if m.blocks[summaryIdx].kind != components.BlockSystem {
		t.Fatalf("expected summary block before final assistant, got %v", m.blocks[summaryIdx].kind)
	}
	if m.blocks[assistantIdx].kind != components.BlockAssistant {
		t.Fatalf("expected assistant block after summary, got %v", m.blocks[assistantIdx].kind)
	}
}

func TestBlocksFromMessagesMergesToolResults(t *testing.T) {
	toolCallID := "call-1"
	prior := []models.AgentMessage{
		models.UserMessage("q"),
		models.NewAgentMessage(models.RoleAssistant,
			models.TextContent{Text: "let me check"},
			models.ToolCallContent{ID: toolCallID, Name: "bash", Arguments: map[string]any{"command": "ls"}},
		),
		models.NewAgentMessage(models.RoleToolResult, models.ToolResultContent{
			ToolCallID: toolCallID,
			Name:       "bash",
			Content:    []models.ContentPart{models.TextContent{Text: "file1"}},
			IsError:    false,
		}),
	}

	blocks := blocksFromMessages(prior)
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks (user, assistant, merged tool), got %d", len(blocks))
	}
	if blocks[0].kind != components.BlockUser || blocks[0].raw != "q" {
		t.Fatalf("first block should be user question, got %+v", blocks[0])
	}
	if blocks[1].kind != components.BlockAssistant || blocks[1].raw != "let me check" {
		t.Fatalf("second block should be assistant reply, got %+v", blocks[1])
	}
	tool := blocks[2]
	if tool.kind != components.BlockTool {
		t.Fatalf("third block should be tool, got %v", tool.kind)
	}
	if tool.toolName != "bash" {
		t.Fatalf("tool name = %q, want bash", tool.toolName)
	}
	if tool.toolResult != "file1" {
		t.Fatalf("tool result = %q, want file1", tool.toolResult)
	}
	if tool.toolErr {
		t.Fatal("tool error should be false")
	}
}

// The idle status line surfaces context budget usage once the agent reports a
// drop limit. Stats() is expensive, so it is sampled only at the AgentEnd
// boundary and cached on the model.
func TestContextPctShownAfterAgentEnd(t *testing.T) {
	m, ag, _ := newTestModel()
	ag.StatsVal = map[string]int{"total": 40000, "drop_limit": 100000}
	m.mainWidth = 80

	m.handleEvent(events.AgentEndEvent{})

	if m.contextPct != 40 {
		t.Fatalf("contextPct = %d, want 40", m.contextPct)
	}
	if view := stripANSI(m.statusLineView()); !strings.Contains(view, "ctx 40%") {
		t.Fatalf("status line should show ctx%%, got %q", view)
	}
}

// A nil/absent drop limit (tests, unconfigured budget) must hide the segment
// rather than divide by zero or render a bogus 0%.
func TestContextPctHiddenWithoutDropLimit(t *testing.T) {
	m, ag, _ := newTestModel()
	ag.StatsVal = nil // FakeAgent default: no stats
	m.mainWidth = 80

	m.handleEvent(events.AgentEndEvent{})

	if m.contextPct != -1 {
		t.Fatalf("contextPct = %d, want -1 (hidden)", m.contextPct)
	}
	if view := stripANSI(m.statusLineView()); strings.Contains(view, "ctx") {
		t.Fatalf("status line must hide ctx%% without a drop limit, got %q", view)
	}
}

// A drop limit of zero is as unusable as a missing one; guard the division.
func TestContextPctHiddenOnZeroDropLimit(t *testing.T) {
	m, ag, _ := newTestModel()
	ag.StatsVal = map[string]int{"total": 5000, "drop_limit": 0}

	m.handleEvent(events.AgentEndEvent{})

	if m.contextPct != -1 {
		t.Fatalf("contextPct = %d, want -1 on zero drop limit", m.contextPct)
	}
}
