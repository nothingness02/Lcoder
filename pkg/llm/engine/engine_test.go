// pkg/llm/engine/engine_test.go
package engine

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/llm/catalog"
	"github.com/lcoder/lcoder/pkg/llm/provider"
	"github.com/lcoder/lcoder/pkg/models"
)

// fakeAdapter emits a fixed event script.
type fakeAdapter struct{ events []provider.Event }

func (f fakeAdapter) Stream(ctx context.Context, conn provider.Conn, req models.TurnRequest) (<-chan provider.Event, error) {
	ch := make(chan provider.Event, len(f.events))
	for _, e := range f.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}

func TestEngineFillsCostOnDone(t *testing.T) {
	cat := catalog.New(catalog.Options{Refresh: false}) // gpt-4o priced 2.5/10 in snapshot
	eng := New(cat)
	eng.SetAdapterFactory(func(route string, marks provider.CacheMarks) provider.Adapter {
		return fakeAdapter{events: []provider.Event{
			{Kind: provider.KindTextDelta, Delta: "hi"},
			{Kind: provider.KindDone,
				Message: models.AgentMessage{Role: models.RoleAssistant},
				Usage:   &models.LLMUsage{PromptTokens: 1_000_000, CompletionTokens: 500_000}},
		}}
	})
	eng.RegisterProvider("openai", provider.Conn{Route: "openai"})

	ch, err := eng.StreamTurn(context.Background(), models.TurnRequest{
		Model: models.ModelRef{Provider: "openai", ID: "gpt-4o"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got *models.LLMUsage
	for ev := range ch {
		if ev.Kind == provider.KindDone {
			got = ev.Usage
		}
	}
	if got == nil {
		t.Fatal("no done event")
	}
	if got.TotalCost != 7.5 {
		t.Fatalf("cost not computed: got %v, want 7.5", got.TotalCost)
	}
	if got.Provider != "openai" || got.Model != "gpt-4o" {
		t.Fatalf("usage provider/model not stamped: %+v", got)
	}
}

func TestEngineRoutesAnthropicCacheMarks(t *testing.T) {
	cat := catalog.New(catalog.Options{Refresh: false})
	eng := New(cat)
	var gotMarks provider.CacheMarks
	eng.SetAdapterFactory(func(route string, marks provider.CacheMarks) provider.Adapter {
		gotMarks = marks
		return fakeAdapter{events: []provider.Event{{Kind: provider.KindDone,
			Message: models.AgentMessage{Role: models.RoleAssistant}}}}
	})
	eng.RegisterProvider("anthropic", provider.Conn{Route: "anthropic"})
	ch, _ := eng.StreamTurn(context.Background(), models.TurnRequest{
		Model:    models.ModelRef{Provider: "anthropic", ID: "claude-sonnet-4-20250514"},
		Messages: []models.AgentMessage{models.UserMessage("hi")},
	})
	for range ch {
	}
	if !gotMarks.System || len(gotMarks.MessageIdx) != 1 {
		t.Fatalf("anthropic cache marks not computed: %+v", gotMarks)
	}
}

func testThinkingCatalog() *catalog.Catalog {
	return catalog.New(catalog.Options{Refresh: false, Overrides: []catalog.Entry{
		{ID: "gpt-5", Provider: "openai", ContextWindow: 400000, Efforts: []string{"low", "medium", "high"}},
		{ID: "grok-4", Provider: "xai", ContextWindow: 256000, Efforts: []string{"low", "high"}, OffEffort: "none"},
	}})
}

func TestResolveThinking(t *testing.T) {
	e := New(testThinkingCatalog())
	e.RegisterProvider("openai", provider.Conn{Route: "openai"})
	e.RegisterProvider("xai", provider.Conn{Route: "openai"})

	// 空配置 → 空
	if got, warn := e.ResolveThinking("openai", "gpt-5", ""); got != "" || warn != "" {
		t.Errorf("empty config: got %q warn %q", got, warn)
	}
	// off + AlwaysThinking → 忽略 + warning
	got, warn := e.ResolveThinking("openai", "gpt-5", "off")
	if got != "" || warn == "" {
		t.Errorf("off on always-thinking: got %q warn %q, want empty+warning", got, warn)
	}
	// off + 有 OffEffort → 保留
	if got, _ := e.ResolveThinking("xai", "grok-4", "off"); got != "off" {
		t.Errorf("grok-4 off: got %q", got)
	}
	// 合法档位 → 保留
	if got, _ := e.ResolveThinking("openai", "gpt-5", "low"); got != "low" {
		t.Errorf("gpt-5 low: got %q", got)
	}
	// 非法档位 → on + warning
	got, warn = e.ResolveThinking("openai", "gpt-5", "extreme")
	if got != "on" || warn == "" {
		t.Errorf("bad effort: got %q warn %q", got, warn)
	}
	// on 原样
	if got, _ := e.ResolveThinking("openai", "gpt-5", "on"); got != "on" {
		t.Errorf("on: got %q", got)
	}
}

// 空 Route 时回退为 provider 名,anthropic 例外仍生效:"off" 不被误判为
// AlwaysThinking 而忽略。
func TestResolveThinkingEmptyRouteFallsBackToProvider(t *testing.T) {
	cat := catalog.New(catalog.Options{Refresh: false, Overrides: []catalog.Entry{
		{ID: "claude-test", Provider: "anthropic", ContextWindow: 200000, Efforts: []string{"low", "high"}},
	}})
	e := New(cat)
	e.RegisterProvider("anthropic", provider.Conn{})

	if got, warn := e.ResolveThinking("anthropic", "claude-test", "off"); got != "off" || warn != "" {
		t.Errorf("off on empty-route anthropic: got %q warn %q, want off+no warning", got, warn)
	}
}

func TestStreamTurnFillsOffEffort(t *testing.T) {
	e := New(testThinkingCatalog())
	e.RegisterProvider("xai", provider.Conn{Route: "openai"})

	// 需要捕获 adapter 收到的 req;用包装 adapter。
	var gotReq models.TurnRequest
	e.SetAdapterFactory(func(route string, marks provider.CacheMarks) provider.Adapter {
		return captureAdapter{&gotReq}
	})
	ch, err := e.StreamTurn(context.Background(), models.TurnRequest{
		Model:    models.ModelRef{Provider: "xai", ID: "grok-4"},
		Thinking: "off",
	})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if gotReq.ThinkingOffEffort != "none" {
		t.Errorf("ThinkingOffEffort = %q, want none", gotReq.ThinkingOffEffort)
	}
}

type captureAdapter struct{ req *models.TurnRequest }

func (c captureAdapter) Stream(ctx context.Context, conn provider.Conn, req models.TurnRequest) (<-chan provider.Event, error) {
	*c.req = req
	ch := make(chan provider.Event, 1)
	ch <- provider.Event{Kind: provider.KindDone, Message: models.AgentMessage{Role: models.RoleAssistant}}
	close(ch)
	return ch, nil
}
