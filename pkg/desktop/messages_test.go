package desktop

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func TestMessagesToUIMergesToolResult(t *testing.T) {
	callID := "call-1"
	msgs := []models.AgentMessage{
		models.UserMessage("q"),
		models.NewAgentMessage(models.RoleAssistant,
			models.TextContent{Text: "let me check"},
			models.ToolCallContent{ID: callID, Name: "bash", Arguments: map[string]any{"command": "ls"}},
		),
		models.NewAgentMessage(models.RoleToolResult, models.ToolResultContent{
			ToolCallID: callID,
			Name:       "bash",
			Content:    []models.ContentPart{models.TextContent{Text: "file1"}},
		}),
	}
	ui := MessagesToUI(msgs)
	if len(ui) != 2 {
		t.Fatalf("expected 2 UI messages, got %d", len(ui))
	}
	assistant := ui[1]
	if len(assistant.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(assistant.ToolCalls))
	}
	if assistant.ToolCalls[0].Result == nil {
		t.Fatal("tool call should have merged result")
	}
	if assistant.ToolCalls[0].Result.Output != "file1" {
		t.Fatalf("result output = %q, want file1", assistant.ToolCalls[0].Result.Output)
	}
}

func TestMessageToUIExtractsThinking(t *testing.T) {
	m := models.NewAgentMessage(models.RoleAssistant,
		models.ThinkingContent{Text: "reasoning"},
		models.TextContent{Text: "answer"},
	)
	ui := MessageToUI(m)
	if ui.Thinking != "reasoning" {
		t.Fatalf("thinking = %q, want reasoning", ui.Thinking)
	}
	if ui.Text != "answer" {
		t.Fatalf("text = %q, want answer", ui.Text)
	}
}
