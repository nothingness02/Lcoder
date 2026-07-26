// pkg/llm/provider/openai_responses_test.go
package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

const responsesSSE = `data: {"type":"response.output_text.delta","delta":"Hello"}

data: {"type":"response.output_text.delta","delta":" world"}

data: {"type":"response.reasoning_summary_text.delta","delta":"thinking..."}

data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"path\":"}

data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"\"a.go\"}"}

data: {"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"a.go\"}"}}

data: {"type":"response.completed","response":{"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"cached_tokens":4}}}}

`

func collectEvents(t *testing.T, ch <-chan Event) []Event {
	t.Helper()
	var evs []Event
	for ev := range ch {
		evs = append(evs, ev)
	}
	return evs
}

func TestResponsesStreamParsing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			t.Errorf("path = %s, want /responses", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(responsesSSE))
	}))
	defer srv.Close()

	ch, err := OpenAIResponses{}.Stream(context.Background(), Conn{BaseURL: srv.URL, APIKey: "k"}, models.TurnRequest{
		Model:    models.ModelRef{ID: "gpt-5-codex"},
		Messages: []models.AgentMessage{models.UserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	evs := collectEvents(t, ch)

	var textDeltas, thinkDeltas, toolDeltas int
	var done *Event
	for i := range evs {
		switch evs[i].Kind {
		case KindTextDelta:
			textDeltas++
		case KindThinkingDelta:
			thinkDeltas++
		case KindToolCallDelta:
			toolDeltas++
		case KindDone:
			done = &evs[i]
		case KindError:
			t.Fatalf("unexpected error event: %v", evs[i].Err)
		}
	}
	if textDeltas != 2 || thinkDeltas != 1 || toolDeltas != 2 {
		t.Errorf("deltas text/think/tool = %d/%d/%d, want 2/1/2", textDeltas, thinkDeltas, toolDeltas)
	}
	if done == nil {
		t.Fatal("no done event")
	}
	if done.Usage == nil || done.Usage.PromptTokens != 10 || done.Usage.CompletionTokens != 5 || done.Usage.CacheReadTokens != 4 {
		t.Errorf("usage = %+v", done.Usage)
	}
	// finalize:thinking + text + tool call
	var thinking, text string
	var calls []models.ToolCallContent
	for _, p := range done.Message.Content {
		switch c := p.(type) {
		case models.ThinkingContent:
			thinking = c.Text
		case models.TextContent:
			text = c.Text
		case models.ToolCallContent:
			calls = append(calls, c)
		}
	}
	if thinking != "thinking..." || text != "Hello world" {
		t.Errorf("thinking=%q text=%q", thinking, text)
	}
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "read_file" || calls[0].Arguments["path"] != "a.go" {
		t.Errorf("tool calls = %+v", calls)
	}
}

func TestResponsesRequestShape(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	defer srv.Close()

	msgs := []models.AgentMessage{
		models.UserMessage("read a.go"),
		models.NewAgentMessage(models.RoleAssistant,
			models.TextContent{Text: "reading"},
			models.ToolCallContent{ID: "call_1", Name: "read_file", Arguments: map[string]any{"path": "a.go"}}),
		models.NewAgentMessage(models.RoleToolResult,
			models.ToolResultContent{ToolCallID: "call_1", Content: []models.ContentPart{models.TextContent{Text: "package main"}}}),
	}
	ch, err := OpenAIResponses{}.Stream(context.Background(), Conn{BaseURL: srv.URL, APIKey: "k"}, models.TurnRequest{
		Model:        models.ModelRef{ID: "gpt-5-codex"},
		SystemPrompt: "you are an agent",
		Messages:     msgs,
		Tools:        []models.ToolDefinition{{Name: "read_file", Description: "read", Parameters: map[string]any{"type": "object"}}},
		Generation:   models.GenerationConfig{MaxTokens: 4096},
		Thinking:     "high",
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	collectEvents(t, ch)

	body := string(captured)
	for _, want := range []string{
		`"instructions":"you are an agent"`,
		`"max_output_tokens":4096`,
		`"effort":"high"`,
		`"type":"function_call"`,
		`"call_id":"call_1"`,
		`"type":"function_call_output"`,
		`"name":"read_file"`,
		`"input_text"`,
		`"output_text"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("request body missing %s:\n%s", want, body)
		}
	}
	// chat completions nested form must not appear
	if strings.Contains(body, `"function":{"name"`) {
		t.Errorf("tools must be flattened, not nested:\n%s", body)
	}
}

func TestResponsesSystemRolePreserved(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	defer srv.Close()

	msgs := []models.AgentMessage{
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: "summary of earlier context"}),
		models.UserMessage("continue"),
	}
	ch, err := OpenAIResponses{}.Stream(context.Background(), Conn{BaseURL: srv.URL, APIKey: "k"}, models.TurnRequest{
		Model:    models.ModelRef{ID: "gpt-5-codex"},
		Messages: msgs,
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	collectEvents(t, ch)

	body := string(captured)
	if !strings.Contains(body, `"role":"system"`) {
		t.Errorf("system message must keep role system:\n%s", body)
	}
	if !strings.Contains(body, `"role":"user"`) {
		t.Errorf("user message must keep role user:\n%s", body)
	}
}

func TestResponsesToolCallFromItemDoneOnly(t *testing.T) {
	// Defensive branch: output_item.done arrives with complete arguments and no
	// preceding function_call_arguments.delta frames.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","id":"fc_9","call_id":"call_9","name":"write_file","arguments":"{\"path\":\"b.go\",\"content\":\"hi\"}"}}

data: {"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}

`))
	}))
	defer srv.Close()

	ch, err := OpenAIResponses{}.Stream(context.Background(), Conn{BaseURL: srv.URL, APIKey: "k"}, models.TurnRequest{
		Model:    models.ModelRef{ID: "gpt-5-codex"},
		Messages: []models.AgentMessage{models.UserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	evs := collectEvents(t, ch)

	var done *Event
	for i := range evs {
		switch evs[i].Kind {
		case KindDone:
			done = &evs[i]
		case KindError:
			t.Fatalf("unexpected error event: %v", evs[i].Err)
		}
	}
	if done == nil {
		t.Fatal("no done event")
	}
	var calls []models.ToolCallContent
	for _, p := range done.Message.Content {
		if c, ok := p.(models.ToolCallContent); ok {
			calls = append(calls, c)
		}
	}
	if len(calls) != 1 {
		t.Fatalf("tool calls = %+v, want exactly 1", calls)
	}
	if calls[0].ID != "call_9" || calls[0].Name != "write_file" {
		t.Errorf("tool call id/name = %q/%q", calls[0].ID, calls[0].Name)
	}
	if calls[0].Arguments["path"] != "b.go" || calls[0].Arguments["content"] != "hi" {
		t.Errorf("tool call arguments = %+v", calls[0].Arguments)
	}
}

func TestResponsesErrorEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"boom\"}}}\n\n"))
	}))
	defer srv.Close()
	ch, _ := OpenAIResponses{}.Stream(context.Background(), Conn{BaseURL: srv.URL}, models.TurnRequest{
		Model:    models.ModelRef{ID: "gpt-5"},
		Messages: []models.AgentMessage{models.UserMessage("hi")},
	})
	evs := collectEvents(t, ch)
	var sawErr bool
	for _, ev := range evs {
		if ev.Kind == KindError {
			sawErr = true
			if !strings.Contains(ev.Err.Message, "boom") {
				t.Errorf("error = %v", ev.Err)
			}
		}
	}
	if !sawErr {
		t.Error("response.failed must surface as KindError")
	}
}

func TestResponsesTopLevelErrorEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"type\":\"error\",\"message\":\"boom2\"}\n\n"))
	}))
	defer srv.Close()
	ch, _ := OpenAIResponses{}.Stream(context.Background(), Conn{BaseURL: srv.URL}, models.TurnRequest{
		Model:    models.ModelRef{ID: "gpt-5"},
		Messages: []models.AgentMessage{models.UserMessage("hi")},
	})
	evs := collectEvents(t, ch)
	var sawErr bool
	for _, ev := range evs {
		if ev.Kind == KindError {
			sawErr = true
			if !strings.Contains(ev.Err.Message, "boom2") {
				t.Errorf("error = %v", ev.Err)
			}
		}
	}
	if !sawErr {
		t.Error("top-level error event must surface as KindError")
	}
}
