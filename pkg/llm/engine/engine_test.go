// pkg/llm/engine/engine_test.go
package engine

import (
	"context"
	"sync"
	"testing"
	"time"

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
	eng.SetAdapterFactory(func(p provider.Protocol, marks provider.CacheMarks) provider.Adapter {
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

// blockingAdapter emits one buffered event and then keeps the channel open
// forever, so engine.forward blocks on the unbuffered out send.
type blockingAdapter struct{}

func (blockingAdapter) Stream(ctx context.Context, conn provider.Conn, req models.TurnRequest) (<-chan provider.Event, error) {
	ch := make(chan provider.Event, 1)
	ch <- provider.Event{Kind: provider.KindTextDelta, Delta: "x"}
	return ch, nil
}

func TestStreamTurnForwardStopsOnCancel(t *testing.T) {
	cat := catalog.New(catalog.Options{Refresh: false})
	eng := New(cat)
	eng.SetAdapterFactory(func(p provider.Protocol, marks provider.CacheMarks) provider.Adapter {
		return blockingAdapter{}
	})
	eng.RegisterProvider("openai", provider.Conn{Route: "openai"})

	ctx, cancel := context.WithCancel(context.Background())
	out, err := eng.StreamTurn(ctx, models.TurnRequest{
		Model: models.ModelRef{Provider: "openai", ID: "gpt-4o"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Never read from out: forward is blocked sending the first event.
	// Cancel first and give forward a moment to observe it, so the send and
	// the cancellation don't race on the final read below.
	cancel()
	time.Sleep(50 * time.Millisecond)
	select {
	case _, ok := <-out:
		if ok {
			t.Fatal("expected closed channel after cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("forward goroutine still blocked after cancel (leak)")
	}
}

func TestEngineRoutesAnthropicCacheMarks(t *testing.T) {
	cat := catalog.New(catalog.Options{Refresh: false})
	eng := New(cat)
	var gotMarks provider.CacheMarks
	eng.SetAdapterFactory(func(p provider.Protocol, marks provider.CacheMarks) provider.Adapter {
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
	e.SetAdapterFactory(func(p provider.Protocol, marks provider.CacheMarks) provider.Adapter {
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

func TestModelMaxInput(t *testing.T) {
	e := New(catalog.New(catalog.Options{Refresh: false, Overrides: []catalog.Entry{
		{ID: "gpt-5", Provider: "openai", ContextWindow: 400000, MaxInput: 272000},
	}}))
	if got := e.ModelMaxInput("openai", "gpt-5"); got != 272000 {
		t.Errorf("ModelMaxInput = %d, want 272000", got)
	}
	if got := e.ModelMaxInput("openai", "no-such-model"); got != 0 {
		t.Errorf("unknown model MaxInput = %d, want 0", got)
	}
}

func TestAdapterFactoryRoutes(t *testing.T) {
	if _, ok := defaultAdapterFactory("anthropic", provider.CacheMarks{}).(provider.Anthropic); !ok {
		t.Error("anthropic route")
	}
	if _, ok := defaultAdapterFactory("openai-responses", provider.CacheMarks{}).(provider.OpenAIResponses); !ok {
		t.Error("openai-responses route")
	}
	if _, ok := defaultAdapterFactory("deepseek", provider.CacheMarks{}).(provider.OpenAICompat); !ok {
		t.Error("default route")
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

func TestProviderRegistrationConcurrentWithStream(t *testing.T) {
	cat := catalog.New(catalog.Options{Refresh: false})
	eng := New(cat)
	eng.SetAdapterFactory(func(p provider.Protocol, marks provider.CacheMarks) provider.Adapter {
		return fakeAdapter{events: []provider.Event{{Kind: provider.KindDone,
			Message: models.AgentMessage{Role: models.RoleAssistant}}}}
	})
	eng.RegisterProvider("openai", provider.Conn{Route: "openai"})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			eng.RegisterProvider("openai", provider.Conn{Route: "openai"})
		}()
		go func() {
			defer wg.Done()
			ch, err := eng.StreamTurn(context.Background(), models.TurnRequest{
				Model: models.ModelRef{Provider: "openai", ID: "gpt-4o"},
			})
			if err == nil {
				for range ch {
				}
			}
		}()
	}
	wg.Wait()
}

func TestAdapterFactorySelectsByProtocol(t *testing.T) {
	cases := []struct {
		name string
		conn provider.Conn
		want provider.Protocol
	}{
		{"route derives openai-chat", provider.Conn{Route: "deepseek"}, provider.ProtocolOpenAIChat},
		{"route derives anthropic", provider.Conn{Route: "anthropic"}, provider.ProtocolAnthropic},
		{"explicit protocol wins over route", provider.Conn{Route: "openai", Protocol: provider.ProtocolOpenAIResponses}, provider.ProtocolOpenAIResponses},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat := catalog.New(catalog.Options{Refresh: false})
			eng := New(cat)
			var got provider.Protocol
			eng.SetAdapterFactory(func(p provider.Protocol, marks provider.CacheMarks) provider.Adapter {
				got = p
				return fakeAdapter{events: []provider.Event{{Kind: provider.KindDone,
					Message: models.AgentMessage{Role: models.RoleAssistant}}}}
			})
			eng.RegisterProvider("p", tc.conn)
			ch, err := eng.StreamTurn(context.Background(), models.TurnRequest{
				Model: models.ModelRef{Provider: "p", ID: "gpt-4o"},
			})
			if err != nil {
				t.Fatal(err)
			}
			for range ch {
			}
			if got != tc.want {
				t.Fatalf("factory got protocol %q, want %q", got, tc.want)
			}
		})
	}
}

// keyFailAdapter fails at establishment for keys in badKeys, tracking the key
// seen on each call.
type keyFailAdapter struct {
	mu      sync.Mutex
	seen    []string
	badKeys map[string]bool
}

func (a *keyFailAdapter) Stream(ctx context.Context, conn provider.Conn, req models.TurnRequest) (<-chan provider.Event, error) {
	a.mu.Lock()
	a.seen = append(a.seen, conn.APIKey)
	a.mu.Unlock()
	if a.badKeys[conn.APIKey] {
		return nil, &provider.EventError{Code: "rate_limit", Message: "slow down"}
	}
	ch := make(chan provider.Event, 1)
	ch <- provider.Event{Kind: provider.KindDone, Message: models.AgentMessage{Role: models.RoleAssistant}}
	close(ch)
	return ch, nil
}

func (a *keyFailAdapter) resetSeen() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seen = nil
}

func (a *keyFailAdapter) seenKeys() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.seen...)
}

func newFailoverEngine(ad provider.Adapter) *Engine {
	eng := New(catalog.New(catalog.Options{Refresh: false}))
	eng.SetAdapterFactory(func(p provider.Protocol, marks provider.CacheMarks) provider.Adapter { return ad })
	eng.RegisterProvider("p", provider.Conn{Route: "openai", APIKeys: []string{"k1", "k2"}})
	return eng
}

func callTurn(eng *Engine) {
	ch, err := eng.StreamTurn(context.Background(), models.TurnRequest{
		Model: models.ModelRef{Provider: "p", ID: "gpt-4o"},
	})
	if err == nil {
		for range ch {
		}
	}
}

func TestFailoverBenchesFailingKey(t *testing.T) {
	ad := &keyFailAdapter{badKeys: map[string]bool{"k1": true}}
	eng := newFailoverEngine(ad)

	// round-robin 顺序 k1,k2,k1,k2,k1:k1 第 3 次失败后被摘除。
	for i := 0; i < 5; i++ {
		callTurn(eng)
	}
	ad.resetSeen()
	callTurn(eng)
	callTurn(eng)
	for _, k := range ad.seenKeys() {
		if k == "k1" {
			t.Fatalf("k1 should be benched after 3 consecutive failures, seen %v", ad.seenKeys())
		}
	}
}

func TestFailoverRestoresAfterCooldown(t *testing.T) {
	ad := &keyFailAdapter{badKeys: map[string]bool{"k1": true}}
	eng := newFailoverEngine(ad)
	eng.cooldown = 30 * time.Millisecond

	for i := 0; i < 5; i++ {
		callTurn(eng)
	}
	time.Sleep(50 * time.Millisecond)
	// 冷却期过后 k1 重新进入轮换:游标先落到 k2,再下一轮回转到 k1。
	callTurn(eng)
	callTurn(eng)
	seen := ad.seenKeys()
	if seen[len(seen)-1] != "k1" {
		t.Fatalf("k1 should re-enter rotation after cooldown, seen %v", seen)
	}
}

func TestConcurrencyGate(t *testing.T) {
	eng := New(catalog.New(catalog.Options{Refresh: false}))
	eng.SetAdapterFactory(func(p provider.Protocol, marks provider.CacheMarks) provider.Adapter {
		return blockingAdapter{}
	})
	eng.RegisterProvider("p", provider.Conn{Route: "openai", MaxConcurrent: 1})

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	out1, err := eng.StreamTurn(ctx1, models.TurnRequest{Model: models.ModelRef{Provider: "p", ID: "gpt-4o"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = out1 // 不读:第一个 turn 占着唯一的槽位

	type res struct {
		ch  <-chan provider.Event
		err error
	}
	resCh := make(chan res, 1)
	go func() {
		ch, err := eng.StreamTurn(context.Background(), models.TurnRequest{Model: models.ModelRef{Provider: "p", ID: "gpt-4o"}})
		resCh <- res{ch, err}
	}()
	select {
	case <-resCh:
		t.Fatal("second StreamTurn should block while the first stream holds the only slot")
	case <-time.After(100 * time.Millisecond):
	}

	// 取消第一个 turn → forward 退出并释放槽位 → 第二个放行。
	cancel1()
	select {
	case r := <-resCh:
		if r.err != nil {
			t.Fatal(r.err)
		}
		for range r.ch {
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second StreamTurn still blocked after the first stream ended")
	}
}
