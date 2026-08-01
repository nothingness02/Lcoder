// pkg/llm/provider/openai_test.go
package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

// sseServer returns an httptest server that replays the given raw SSE body.
func sseServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func collect(t *testing.T, ch <-chan Event) []Event {
	t.Helper()
	var out []Event
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

func TestOpenAIStreamTextAndUsage(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{}}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n\n" +
		"data: [DONE]\n\n"
	srv := sseServer(t, body)

	ad := OpenAICompat{}
	ch, err := ad.Stream(context.Background(),
		Conn{BaseURL: srv.URL, APIKey: "k"},
		models.TurnRequest{Model: models.ModelRef{Provider: "openai", ID: "gpt-4o"},
			Messages: []models.AgentMessage{models.UserMessage("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	if evs[0].Kind != KindStart {
		t.Fatalf("first event %v", evs[0].Kind)
	}
	var text string
	var done *Event
	for i := range evs {
		switch evs[i].Kind {
		case KindTextDelta:
			text += evs[i].Delta
		case KindDone:
			done = &evs[i]
		}
	}
	if text != "Hello" {
		t.Errorf("text=%q", text)
	}
	if done == nil || done.Usage == nil || done.Usage.PromptTokens != 5 || done.Usage.CompletionTokens != 2 {
		t.Fatalf("done/usage wrong: %+v", done)
	}
	if done.Message.Text() != "Hello" {
		t.Errorf("done message text=%q", done.Message.Text())
	}
}

func TestOpenAIStreamDeepSeekCacheUsage(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{}}],\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":5,\"total_tokens\":1005,\"prompt_cache_hit_tokens\":800,\"prompt_cache_miss_tokens\":200}}\n\n" +
		"data: [DONE]\n\n"
	srv := sseServer(t, body)

	ad := OpenAICompat{}
	ch, err := ad.Stream(context.Background(),
		Conn{BaseURL: srv.URL, APIKey: "k"},
		models.TurnRequest{Model: models.ModelRef{Provider: "deepseek", ID: "deepseek-v4-flash"}})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)
	var done *Event
	for i := range evs {
		if evs[i].Kind == KindDone {
			done = &evs[i]
		}
	}
	if done == nil || done.Usage == nil {
		t.Fatalf("expected done with usage, got %+v", done)
	}
	if done.Usage.CacheReadTokens != 800 {
		t.Fatalf("expected cache read 800, got %d", done.Usage.CacheReadTokens)
	}
	if done.Usage.PromptTokens != 1000 {
		t.Fatalf("expected prompt 1000, got %d", done.Usage.PromptTokens)
	}
}

func TestOpenAIStreamOpenAICachedTokensDetails(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{}}],\"usage\":{\"prompt_tokens\":500,\"completion_tokens\":3,\"total_tokens\":503,\"prompt_tokens_details\":{\"cached_tokens\":400}}}\n\n" +
		"data: [DONE]\n\n"
	srv := sseServer(t, body)

	ad := OpenAICompat{}
	ch, err := ad.Stream(context.Background(),
		Conn{BaseURL: srv.URL, APIKey: "k"},
		models.TurnRequest{Model: models.ModelRef{Provider: "openai", ID: "gpt-4o"}})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)
	var done *Event
	for i := range evs {
		if evs[i].Kind == KindDone {
			done = &evs[i]
		}
	}
	if done == nil || done.Usage == nil || done.Usage.CacheReadTokens != 400 {
		t.Fatalf("expected cache read 400, got %+v", done)
	}
}

func TestOpenAIStreamChoiceLevelUsage(t *testing.T) {
	// Kimi Code (and some other OpenAI-compatible endpoints) reports usage on the
	// final choice rather than at the chunk top level.
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\",\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":3,\"total_tokens\":15,\"cached_tokens\":10,\"prompt_tokens_details\":{\"cached_tokens\":10}}}]}\n\n" +
		"data: [DONE]\n\n"
	srv := sseServer(t, body)

	ad := OpenAICompat{}
	ch, err := ad.Stream(context.Background(),
		Conn{BaseURL: srv.URL, APIKey: "k"},
		models.TurnRequest{Model: models.ModelRef{Provider: "openai", ID: "kimi-for-coding"}})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)
	var done *Event
	for i := range evs {
		if evs[i].Kind == KindDone {
			done = &evs[i]
		}
	}
	if done == nil || done.Usage == nil {
		t.Fatalf("expected done with usage, got %+v", done)
	}
	if done.Usage.PromptTokens != 12 {
		t.Fatalf("expected prompt 12, got %d", done.Usage.PromptTokens)
	}
	if done.Usage.CacheReadTokens != 10 {
		t.Fatalf("expected cache read 10, got %d", done.Usage.CacheReadTokens)
	}
}

func TestOpenAIStreamToolCallFragments(t *testing.T) {
	// Tool call arguments arrive split across chunks; they must accumulate by index.
	body := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"pa\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"th\\\":\\\"x\\\"}\"}}]}}]}\n\n" +
		"data: [DONE]\n\n"
	srv := sseServer(t, body)

	ad := OpenAICompat{}
	ch, _ := ad.Stream(context.Background(),
		Conn{BaseURL: srv.URL, APIKey: "k"},
		models.TurnRequest{Model: models.ModelRef{Provider: "openai", ID: "gpt-4o"}})
	evs := collect(t, ch)

	var done *Event
	for i := range evs {
		if evs[i].Kind == KindDone {
			done = &evs[i]
		}
	}
	if done == nil {
		t.Fatal("no done event")
	}
	calls := done.Message.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("want 1 tool call, got %d", len(calls))
	}
	if calls[0].Name != "read" || calls[0].ID != "c1" {
		t.Errorf("tool call meta wrong: %+v", calls[0])
	}
	if calls[0].Arguments["path"] != "x" {
		t.Errorf("accumulated args wrong: %+v", calls[0].Arguments)
	}
}

func TestOpenAIStreamThinkingDelta(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"ponder\"}}]}\n\n" +
		"data: [DONE]\n\n"
	srv := sseServer(t, body)
	ad := OpenAICompat{}
	ch, _ := ad.Stream(context.Background(), Conn{BaseURL: srv.URL},
		models.TurnRequest{Model: models.ModelRef{Provider: "deepseek", ID: "deepseek-reasoner"}})
	evs := collect(t, ch)
	var thinking string
	for _, e := range evs {
		if e.Kind == KindThinkingDelta {
			thinking += e.Delta
		}
	}
	if thinking != "ponder" {
		t.Errorf("thinking=%q", thinking)
	}
}

func TestOpenAIThinkingMapping(t *testing.T) {
	cases := []struct {
		name       string
		thinking   string
		offEffort  string
		wantEffort string // "" = 字段不应出现
	}{
		{"empty sends nothing", "", "", ""},
		{"on sends nothing", "on", "", ""},
		{"level passes through", "low", "", "low"},
		{"off with off-effort", "off", "none", "none"},
		{"off without off-effort sends nothing", "off", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := captureRequestBody(t, OpenAICompat{}, models.TurnRequest{
				Model:    models.ModelRef{ID: "gpt-5"},
				Messages: []models.AgentMessage{models.UserMessage("hi")},
				Thinking: tc.thinking, ThinkingOffEffort: tc.offEffort,
			})
			got, exists := body["reasoning_effort"]
			if tc.wantEffort == "" && exists {
				t.Errorf("reasoning_effort should be absent, got %v", got)
			}
			if tc.wantEffort != "" && got != tc.wantEffort {
				t.Errorf("reasoning_effort = %v, want %q", got, tc.wantEffort)
			}
		})
	}
}

func TestOpenAIStreamHTTPErrorClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
	}))
	t.Cleanup(srv.Close)
	ad := OpenAICompat{}
	_, err := ad.Stream(context.Background(), Conn{BaseURL: srv.URL},
		models.TurnRequest{Model: models.ModelRef{Provider: "openai", ID: "gpt-4o"}})
	var pe *EventError
	if !errors.As(err, &pe) || pe.Code != "rate_limit" {
		t.Fatalf("want rate_limit EventError from Stream, got %v", err)
	}
}

func TestOpenAISendsIncludeUsage(t *testing.T) {
	body := captureRequestBody(t, OpenAICompat{}, models.TurnRequest{
		Model:    models.ModelRef{ID: "gpt-4o"},
		Messages: []models.AgentMessage{models.UserMessage("hi")},
	})
	so, ok := body["stream_options"].(map[string]any)
	if !ok || so["include_usage"] != true {
		t.Fatalf("stream_options.include_usage missing: %v", body["stream_options"])
	}
}

func TestOpenAIStreamContextOverflowClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"This model's maximum context length is 128000 tokens.","code":"context_length_exceeded"}}`))
	}))
	t.Cleanup(srv.Close)
	ad := OpenAICompat{}
	_, err := ad.Stream(context.Background(), Conn{BaseURL: srv.URL},
		models.TurnRequest{Model: models.ModelRef{Provider: "openai", ID: "gpt-4o"}})
	var pe *EventError
	if !errors.As(err, &pe) || pe.Code != "context_overflow" {
		t.Fatalf("want context_overflow EventError, got %v", err)
	}
}
