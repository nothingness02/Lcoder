// pkg/llm/provider/openai_test.go
package provider

import (
	"context"
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
		Conn{BaseURL: srv.URL, APIKey: "k", Route: "openai"},
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
		Conn{BaseURL: srv.URL, APIKey: "k", Route: "deepseek"},
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
		Conn{BaseURL: srv.URL, APIKey: "k", Route: "openai"},
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

func TestOpenAIStreamToolCallFragments(t *testing.T) {
	// Tool call arguments arrive split across chunks; they must accumulate by index.
	body := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"pa\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"th\\\":\\\"x\\\"}\"}}]}}]}\n\n" +
		"data: [DONE]\n\n"
	srv := sseServer(t, body)

	ad := OpenAICompat{}
	ch, _ := ad.Stream(context.Background(),
		Conn{BaseURL: srv.URL, APIKey: "k", Route: "openai"},
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
	ch, _ := ad.Stream(context.Background(), Conn{BaseURL: srv.URL, Route: "deepseek"},
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

func TestOpenAIStreamHTTPErrorClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
	}))
	t.Cleanup(srv.Close)
	ad := OpenAICompat{}
	ch, err := ad.Stream(context.Background(), Conn{BaseURL: srv.URL, Route: "openai"},
		models.TurnRequest{Model: models.ModelRef{Provider: "openai", ID: "gpt-4o"}})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)
	last := evs[len(evs)-1]
	if last.Kind != KindError || last.Err == nil || last.Err.Code != "rate_limit" {
		t.Fatalf("want rate_limit error, got %+v", last)
	}
}
