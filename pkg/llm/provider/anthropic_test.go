// pkg/llm/provider/anthropic_test.go
package provider

import (
	"context"
	"strings"
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

// TestApplyMessageCacheMarksToolResult is the core prompt-cache regression: in an
// agent tool loop the tail message is a pure tool_result with no text block, so
// marking only the first text block silently dropped the anchor and re-billed the
// whole transcript as uncached input on every step.
func TestApplyMessageCacheMarksToolResult(t *testing.T) {
	msgs := []map[string]any{
		{"role": "user", "content": []map[string]any{
			{"type": "tool_result", "tool_use_id": "t1", "content": "file body"},
		}},
	}
	applyMessageCacheMarks(msgs, []int{0})
	blocks := msgs[0]["content"].([]map[string]any)
	if blocks[0]["cache_control"] == nil {
		t.Fatalf("tool_result block must carry cache_control, got %v", blocks[0])
	}
}

// TestApplyMessageCacheMarksLastBlock pins the marker to the LAST cacheable block
// of a message. Anthropic caches the prefix up to and including the marked block,
// so marking an earlier block leaves the rest of that message uncached.
func TestApplyMessageCacheMarksLastBlock(t *testing.T) {
	msgs := []map[string]any{
		{"role": "assistant", "content": []map[string]any{
			{"type": "text", "text": "let me read that file"},
			{"type": "tool_use", "id": "t1", "name": "read_file", "input": map[string]any{}},
		}},
	}
	applyMessageCacheMarks(msgs, []int{0})
	blocks := msgs[0]["content"].([]map[string]any)
	if blocks[1]["cache_control"] == nil {
		t.Fatalf("last block must carry cache_control, got %v", blocks[1])
	}
	if blocks[0]["cache_control"] != nil {
		t.Fatalf("only the last block should be marked, got %v", blocks[0])
	}
}

// TestApplyMessageCacheMarksSkipsThinking guards the wire contract: Anthropic
// rejects cache_control on a thinking block, so a trailing thinking block must
// fall back to the previous cacheable block rather than produce a 400.
func TestApplyMessageCacheMarksSkipsThinking(t *testing.T) {
	msgs := []map[string]any{
		{"role": "assistant", "content": []map[string]any{
			{"type": "text", "text": "answer"},
			{"type": "thinking", "thinking": "reasoning trace"},
		}},
	}
	applyMessageCacheMarks(msgs, []int{0})
	blocks := msgs[0]["content"].([]map[string]any)
	if blocks[1]["cache_control"] != nil {
		t.Fatalf("thinking block must not be marked, got %v", blocks[1])
	}
	if blocks[0]["cache_control"] == nil {
		t.Fatalf("expected fallback to the text block, got %v", blocks[0])
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
		Conn{BaseURL: srv.URL, APIKey: "k"},
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

	// 小 max_tokens:budget 钉在 API 下限 1024
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
	if b, _ := th["budget_tokens"].(float64); b != float64(1024) {
		t.Errorf("budget = %v, want 1024 (API minimum)", th["budget_tokens"])
	}

	// max_tokens 不足以容纳 API 下限 1024:省略 thinking 字段
	body = captureRequestBody(t, Anthropic{}, models.TurnRequest{
		Model:      models.ModelRef{ID: "claude-sonnet-4"},
		Messages:   userMsg,
		Generation: models.GenerationConfig{MaxTokens: 1024},
		Thinking:   "on",
	})
	if _, exists := body["thinking"]; exists {
		t.Errorf("max_tokens=1024 cannot satisfy the 1024 budget minimum; thinking must be omitted, got %v", body["thinking"])
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
		Conn{BaseURL: srv.URL, APIKey: "k"},
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

func TestAnthropicStreamErrorEvent(t *testing.T) {
	body := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"
	srv := sseServer(t, body)

	ad := Anthropic{}
	ch, err := ad.Stream(context.Background(),
		Conn{BaseURL: srv.URL, APIKey: "k"},
		models.TurnRequest{Model: models.ModelRef{Provider: "anthropic", ID: "claude-sonnet-4"},
			Messages: []models.AgentMessage{models.UserMessage("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	var errEv *Event
	for i := range evs {
		if evs[i].Kind == KindError {
			errEv = &evs[i]
		}
		if evs[i].Kind == KindDone {
			t.Fatalf("stream error must not be reported as done: %+v", evs[i])
		}
	}
	if errEv == nil || errEv.Err == nil {
		t.Fatal("no error event emitted for anthropic error frame")
	}
	if errEv.Err.Code != "rate_limit" {
		t.Fatalf("overloaded_error should classify as rate_limit, got %q", errEv.Err.Code)
	}
	if !strings.Contains(errEv.Err.Message, "Overloaded") {
		t.Fatalf("error message lost: %q", errEv.Err.Message)
	}
}
