// pkg/llm/provider/convert_test.go
package provider

import (
	"reflect"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func TestOpenAIMessagesUserText(t *testing.T) {
	msgs := []models.AgentMessage{
		models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hi"}),
	}
	got := openAIMessages(msgs)
	want := []map[string]any{{"role": "user", "content": "hi"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestOpenAIMessagesAssistantToolCall(t *testing.T) {
	msgs := []models.AgentMessage{
		models.NewAgentMessage(models.RoleAssistant,
			models.ToolCallContent{ID: "c1", Name: "read", Arguments: map[string]any{"path": "x"}}),
	}
	got := openAIMessages(msgs)
	if got[0]["role"] != "assistant" {
		t.Fatalf("role=%v", got[0]["role"])
	}
	tcs, ok := got[0]["tool_calls"].([]map[string]any)
	if !ok || len(tcs) != 1 {
		t.Fatalf("tool_calls=%v", got[0]["tool_calls"])
	}
	fn := tcs[0]["function"].(map[string]any)
	if fn["name"] != "read" || fn["arguments"] != `{"path":"x"}` {
		t.Errorf("function=%v", fn)
	}
}

func TestOpenAIMessagesToolResult(t *testing.T) {
	msgs := []models.AgentMessage{
		models.NewAgentMessage(models.RoleToolResult,
			models.ToolResultContent{ToolCallID: "c1", Content: []models.ContentPart{models.TextContent{Text: "ok"}}}),
	}
	got := openAIMessages(msgs)
	want := []map[string]any{{"role": "tool", "tool_call_id": "c1", "content": "ok"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestOpenAIMessagesDropsEmptySystem(t *testing.T) {
	msgs := []models.AgentMessage{
		models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hi"}),
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: ""}),
	}
	got := openAIMessages(msgs)
	if len(got) != 1 || got[0]["role"] != "user" {
		t.Fatalf("expected empty system message to be dropped, got %v", got)
	}
}

func TestOpenAIMessagesDropsEmptyUser(t *testing.T) {
	msgs := []models.AgentMessage{
		models.NewAgentMessage(models.RoleUser, models.TextContent{Text: ""}),
		models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "hello"}),
	}
	got := openAIMessages(msgs)
	if len(got) != 1 || got[0]["role"] != "assistant" {
		t.Fatalf("expected empty user message to be dropped, got %v", got)
	}
}

func TestOpenAITools(t *testing.T) {
	tools := []models.ToolDefinition{{Name: "read", Description: "read a file", Parameters: map[string]any{"type": "object"}}}
	got := openAITools(tools)
	if len(got) != 1 || got[0]["type"] != "function" {
		t.Fatalf("got %v", got)
	}
	fn := got[0]["function"].(map[string]any)
	if fn["name"] != "read" || fn["description"] != "read a file" {
		t.Errorf("function=%v", fn)
	}
}
