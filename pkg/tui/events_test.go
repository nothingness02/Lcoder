package tui

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/tui/components"
)

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
