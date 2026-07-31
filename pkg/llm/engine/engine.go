// pkg/llm/engine/engine.go
package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/lcoder/lcoder/pkg/llm/catalog"
	"github.com/lcoder/lcoder/pkg/llm/pricing"
	"github.com/lcoder/lcoder/pkg/llm/provider"
	"github.com/lcoder/lcoder/pkg/models"
)

// AdapterFactory builds an adapter for a route, given precomputed cache marks.
type AdapterFactory func(route string, marks provider.CacheMarks) provider.Adapter

// Engine routes turns to provider adapters in-process.
type Engine struct {
	mu         sync.RWMutex // guards providers
	providers  map[string]provider.Conn
	catalog    *catalog.Catalog
	newAdapter AdapterFactory
}

// New builds an engine over a catalog with the default adapter factory.
func New(cat *catalog.Catalog) *Engine {
	return &Engine{
		providers:  map[string]provider.Conn{},
		catalog:    cat,
		newAdapter: defaultAdapterFactory,
	}
}

func defaultAdapterFactory(route string, marks provider.CacheMarks) provider.Adapter {
	switch route {
	case "anthropic":
		return provider.Anthropic{Marks: marks}
	case "openai-responses":
		return provider.OpenAIResponses{}
	default:
		return provider.OpenAICompat{}
	}
}

// SetAdapterFactory overrides adapter construction (used by tests / llmtest).
func (e *Engine) SetAdapterFactory(f AdapterFactory) { e.newAdapter = f }

// RegisterProvider stores or replaces an in-memory provider connection.
func (e *Engine) RegisterProvider(name string, conn provider.Conn) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.providers[name] = conn
}

// ListModels returns the catalog's model list.
func (e *Engine) ListModels() []models.ModelInfo { return e.catalog.List() }

// ModelWindow returns the catalog context window for provider/model (0 if unknown).
func (e *Engine) ModelWindow(prov, model string) int { return e.catalog.Window(prov, model) }

// ModelMaxOutput returns the catalog single-response output ceiling for
// provider/model (0 if unknown).
func (e *Engine) ModelMaxOutput(prov, model string) int { return e.catalog.MaxOutput(prov, model) }

// ModelMaxInput returns the catalog prompt cap for provider/model (0 = no
// separate cap; use the context window).
func (e *Engine) ModelMaxInput(prov, model string) int { return e.catalog.MaxInput(prov, model) }

// ResolveThinking validates a configured thinking value against the catalog
// and returns the value to put on turn requests, plus a user-facing warning
// when the config had to be adjusted. "" means "send no thinking field".
func (e *Engine) ResolveThinking(provider, model, want string) (resolved, warning string) {
	t := strings.ToLower(strings.TrimSpace(want))
	if t == "" {
		return "", ""
	}
	// 与 StreamTurn 一致:空 Route 回退为 provider 名,否则 anthropic 例外失效。
	e.mu.RLock()
	route := e.providers[provider].Route
	e.mu.RUnlock()
	if route == "" {
		route = provider
	}
	spec := e.catalog.ThinkingSpec(route, provider, model)
	switch t {
	case "off":
		if spec.AlwaysThinking {
			return "", fmt.Sprintf("模型 %s 的 thinking 不可关闭,已忽略 thinking: off", model)
		}
		return "off", ""
	case "on":
		return "on", ""
	default:
		if len(spec.Efforts) > 0 {
			if slices.Contains(spec.Efforts, t) {
				return t, ""
			}
			return "on", fmt.Sprintf("模型 %s 未声明 thinking 档位 %q(支持 %v),已回退为 on", model, t, spec.Efforts)
		}
		return t, ""
	}
}

func (e *Engine) resolveProvider(ref models.ModelRef) string {
	if ref.Provider != "" {
		return ref.Provider
	}
	for _, m := range e.catalog.List() {
		if m.ID == ref.ID {
			return m.Provider
		}
	}
	return ""
}

// StreamTurn selects an adapter, starts the provider stream, and returns a
// channel of normalized events with cost filled in on the done event.
func (e *Engine) StreamTurn(ctx context.Context, req models.TurnRequest) (<-chan provider.Event, error) {
	prov := e.resolveProvider(req.Model)
	e.mu.RLock()
	conn := e.providers[prov]
	e.mu.RUnlock()
	if conn.Route == "" {
		conn.Route = prov
	}
	anthropic := conn.Route == "anthropic"
	marks := provider.ComputeCacheMarks(req.Cache, req.CacheBreakpoints, len(req.Messages), anthropic)
	conn.BaseURL = provider.ResolveBaseURL(conn)

	if req.Thinking == "off" {
		req.ThinkingOffEffort = e.catalog.ThinkingSpec(conn.Route, prov, req.Model.ID).OffEffort
	}

	adapter := e.newAdapter(conn.Route, marks)
	src, err := adapter.Stream(ctx, conn, req)
	if err != nil {
		return nil, err
	}
	out := make(chan provider.Event)
	go e.forward(ctx, prov, req.Model.ID, src, out)
	return out, nil
}

// forward copies events through, computing cost on the done event. It stops
// when ctx is cancelled so an abandoned consumer cannot leak the goroutine.
func (e *Engine) forward(ctx context.Context, prov, model string, src <-chan provider.Event, out chan<- provider.Event) {
	defer close(out)
	table := e.catalog.PriceTable()
	for ev := range src {
		if ev.Kind == provider.KindDone && ev.Usage != nil {
			u := ev.Usage
			u.Provider = prov
			u.Model = model
			cb := pricing.EstimateCost(table, prov, model,
				u.PromptTokens, u.CompletionTokens, u.CacheReadTokens, u.CacheWriteTokens)
			u.PromptCost = cb.PromptCost
			u.CompletionCost = cb.CompletionCost
			u.CacheReadCost = cb.CacheReadCost
			u.CacheWriteCost = cb.CacheWriteCost
			u.TotalCost = cb.TotalCost
		}
		select {
		case out <- ev:
		case <-ctx.Done():
			return
		}
	}
}
