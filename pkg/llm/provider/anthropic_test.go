// pkg/llm/provider/anthropic_test.go
package provider

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

// TestAnthropicMessagesSystemBecomesUser guards against silently dropping a
// system-role message that appears in the conversation stream (the transient
// compaction summary). The Anthropic messages array has no system role, so it
// must be transmitted as a user turn rather than discarded.
func TestAnthropicMessagesSystemBecomesUser(t *testing.T) {
	msgs := []models.AgentMessage{
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: "[Summary of earlier conversation]"}),
		models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "next"}),
	}
	got := anthropicMessages(msgs)
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d: %v", len(got), got)
	}
	if got[0]["role"] != "user" {
		t.Fatalf("summary message must be a user turn, got role %v", got[0]["role"])
	}
	blocks, ok := got[0]["content"].([]map[string]any)
	if !ok || len(blocks) != 1 || blocks[0]["text"] != "[Summary of earlier conversation]" {
		t.Fatalf("summary text not preserved: %v", got[0]["content"])
	}
}

func TestAnthropicStreamTextThinkingUsage(t *testing.T) {
	body := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"hmm\"}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":3}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	srv := sseServer(t, body)

	ad := Anthropic{}
	ch, err := ad.Stream(context.Background(),
		Conn{BaseURL: srv.URL, APIKey: "k", Route: "anthropic"},
		models.TurnRequest{Model: models.ModelRef{Provider: "anthropic", ID: "claude-sonnet-4"},
			Messages: []models.AgentMessage{models.UserMessage("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	var text, thinking string
	var done *Event
	for i := range evs {
		switch evs[i].Kind {
		case KindTextDelta:
			text += evs[i].Delta
		case KindThinkingDelta:
			thinking += evs[i].Delta
		case KindDone:
			done = &evs[i]
		}
	}
	if text != "Hi" || thinking != "hmm" {
		t.Errorf("text=%q thinking=%q", text, thinking)
	}
	if done == nil || done.Usage == nil || done.Usage.PromptTokens != 10 || done.Usage.CompletionTokens != 3 {
		t.Fatalf("usage wrong: %+v", done)
	}
	if done.Message.Text() != "Hi" || done.Message.Thinking() != "hmm" {
		t.Errorf("final message wrong: %+v", done.Message)
	}
}

func TestAnthropicThinkingMapping(t *testing.T) {
	userMsg := []models.AgentMessage{models.UserMessage("hi")}

	// on → enabled, budget = max(1024, maxTokens/2) 且 < max_tokens
	body := captureRequestBody(t, Anthropic{}, models.TurnRequest{
		Model:      models.ModelRef{ID: "claude-sonnet-4"},
		Messages:   userMsg,
		Generation: models.GenerationConfig{MaxTokens: 16384},
		Thinking:   "on",
	})
	th, ok := body["thinking"].(map[string]any)
	if !ok || th["type"] != "enabled" {
		t.Fatalf("thinking = %v, want enabled", body["thinking"])
	}
	if th["budget_tokens"] != float64(8192) {
		t.Errorf("budget = %v, want 8192 (16384/2)", th["budget_tokens"])
	}

	// off → disabled
	body = captureRequestBody(t, Anthropic{}, models.TurnRequest{
		Model:      models.ModelRef{ID: "claude-sonnet-4"},
		Messages:   userMsg,
		Generation: models.GenerationConfig{MaxTokens: 16384},
		Thinking:   "off",
	})
	th, _ = body["thinking"].(map[string]any)
	if th == nil || th["type"] != "disabled" {
		t.Errorf("thinking = %v, want disabled", body["thinking"])
	}

	// 空 → 无字段
	body = captureRequestBody(t, Anthropic{}, models.TurnRequest{
		Model:    models.ModelRef{ID: "claude-sonnet-4"},
		Messages: userMsg,
	})
	if _, exists := body["thinking"]; exists {
		t.Error("empty thinking must not send the field")
	}

	// 小 max_tokens:budget 仍 < max_tokens
	body = captureRequestBody(t, Anthropic{}, models.TurnRequest{
		Model:      models.ModelRef{ID: "claude-sonnet-4"},
		Messages:   userMsg,
		Generation: models.GenerationConfig{MaxTokens: 1500},
		Thinking:   "on",
	})
	th, _ = body["thinking"].(map[string]any)
	if th == nil {
		t.Fatal("thinking missing")
	}
	if b, _ := th["budget_tokens"].(float64); b >= 1500 {
		t.Errorf("budget %v must be < max_tokens 1500", th["budget_tokens"])
	}
}

func TestAnthropicStreamToolUse(t *testing.T) {
	body := "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tu1\",\"name\":\"read\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"x\\\"}\"}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	srv := sseServer(t, body)

	ad := Anthropic{}
	ch, _ := ad.Stream(context.Background(),
		Conn{BaseURL: srv.URL, APIKey: "k", Route: "anthropic"},
		models.TurnRequest{Model: models.ModelRef{Provider: "anthropic", ID: "claude-sonnet-4"}})
	evs := collect(t, ch)

	var done *Event
	for i := range evs {
		if evs[i].Kind == KindDone {
			done = &evs[i]
		}
	}
	calls := done.Message.ToolCalls()
	if len(calls) != 1 || calls[0].ID != "tu1" || calls[0].Name != "read" {
		t.Fatalf("tool call meta wrong: %+v", calls)
	}
	if calls[0].Arguments["path"] != "x" {
		t.Errorf("args=%+v", calls[0].Arguments)
	}
}
