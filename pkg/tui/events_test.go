package tui

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
)

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
	if last.kind != blockSystem {
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
	if m.blocks[summaryIdx].kind != blockSystem {
		t.Fatalf("expected summary block before final assistant, got %v", m.blocks[summaryIdx].kind)
	}
	if m.blocks[assistantIdx].kind != blockAssistant {
		t.Fatalf("expected assistant block after summary, got %v", m.blocks[assistantIdx].kind)
	}
}
