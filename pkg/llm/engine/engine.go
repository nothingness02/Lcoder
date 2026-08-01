// pkg/llm/engine/engine.go
package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lcoder/lcoder/pkg/llm/catalog"
	"github.com/lcoder/lcoder/pkg/llm/pricing"
	"github.com/lcoder/lcoder/pkg/llm/provider"
	"github.com/lcoder/lcoder/pkg/models"
)

// AdapterFactory builds an adapter for a protocol, given precomputed cache marks.
type AdapterFactory func(p provider.Protocol, marks provider.CacheMarks) provider.Adapter

// credential is one API key in a failover pool. After failThreshold
// consecutive establishment failures it is benched until the cooldown elapses
// (no active probing — recovery is time-based, like higress's cooldown mode).
type credential struct {
	key              string
	failures         int
	unavailableUntil time.Time
}

// credPool rotates a provider's API keys round-robin, skipping benched ones.
type credPool struct {
	creds []*credential
	next  int
}

// Engine routes turns to provider adapters in-process.
type Engine struct {
	mu         sync.RWMutex // guards providers, pools and sems
	providers  map[string]provider.Conn
	pools      map[string]*credPool
	sems       map[string]chan struct{} // per-provider concurrency gate
	catalog    *catalog.Catalog
	newAdapter AdapterFactory

	failThreshold int
	cooldown      time.Duration
	now           func() time.Time // test hook
}

// New builds an engine over a catalog with the default adapter factory.
func New(cat *catalog.Catalog) *Engine {
	return &Engine{
		providers:     map[string]provider.Conn{},
		pools:         map[string]*credPool{},
		sems:          map[string]chan struct{}{},
		catalog:       cat,
		newAdapter:    defaultAdapterFactory,
		failThreshold: 3,
		cooldown:      time.Minute,
		now:           time.Now,
	}
}

func defaultAdapterFactory(p provider.Protocol, marks provider.CacheMarks) provider.Adapter {
	switch p {
	case provider.ProtocolAnthropic:
		return provider.Anthropic{Marks: marks}
	case provider.ProtocolOpenAIResponses:
		return provider.OpenAIResponses{}
	default:
		return provider.OpenAICompat{}
	}
}

// SetAdapterFactory overrides adapter construction (used by tests / llmtest).
func (e *Engine) SetAdapterFactory(f AdapterFactory) { e.newAdapter = f }

// RegisterProvider stores or replaces an in-memory provider connection,
// rebuilding its failover pool when APIKeys is set.
func (e *Engine) RegisterProvider(name string, conn provider.Conn) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.providers[name] = conn
	if conn.MaxConcurrent > 0 {
		e.sems[name] = make(chan struct{}, conn.MaxConcurrent)
	} else {
		delete(e.sems, name)
	}
	if len(conn.APIKeys) == 0 {
		delete(e.pools, name)
		return
	}
	pool := &credPool{}
	seen := map[string]bool{}
	add := func(k string) {
		if k != "" && !seen[k] {
			seen[k] = true
			pool.creds = append(pool.creds, &credential{key: k})
		}
	}
	add(conn.APIKey)
	for _, k := range conn.APIKeys {
		add(k)
	}
	if len(pool.creds) == 0 {
		delete(e.pools, name)
		return
	}
	e.pools[name] = pool
}

// selectCredential returns the next usable key for prov, or "" when the
// provider has no failover pool (the caller keeps conn.APIKey). Benched keys
// are skipped; when every key is benched it falls back to plain round-robin
// rather than stalling traffic.
func (e *Engine) selectCredential(prov string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	pool := e.pools[prov]
	if pool == nil || len(pool.creds) == 0 {
		return ""
	}
	now := e.now()
	n := len(pool.creds)
	for i := 0; i < n; i++ {
		idx := (pool.next + i) % n
		c := pool.creds[idx]
		if c.failures >= e.failThreshold && now.Before(c.unavailableUntil) {
			continue
		}
		pool.next = (idx + 1) % n
		return c.key
	}
	key := pool.creds[pool.next%n].key
	pool.next = (pool.next + 1) % n
	return key
}

// reportCredential records the establishment outcome for one key: failure
// increments the consecutive-failure count (benching at the threshold);
// success resets it.
func (e *Engine) reportCredential(prov, key string, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	pool := e.pools[prov]
	if pool == nil {
		return
	}
	for _, c := range pool.creds {
		if c.key != key {
			continue
		}
		if ok {
			c.failures = 0
			c.unavailableUntil = time.Time{}
		} else {
			c.failures++
			if c.failures >= e.failThreshold {
				c.unavailableUntil = e.now().Add(e.cooldown)
			}
		}
		return
	}
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
func (e *Engine) ResolveThinking(prov, model, want string) (resolved, warning string) {
	t := strings.ToLower(strings.TrimSpace(want))
	if t == "" {
		return "", ""
	}
	// 与 StreamTurn 一致:空 Route 回退为 provider 名,否则 anthropic 例外失效。
	e.mu.RLock()
	conn := e.providers[prov]
	e.mu.RUnlock()
	route := conn.Route
	if route == "" {
		route = prov
	}
	// AlwaysThinking 的判别看线协议(该协议有没有 off 编码),不是 provider 名。
	proto := conn.Protocol
	if proto == "" {
		proto = provider.ProtocolForRoute(route)
	}
	spec := e.catalog.ThinkingSpec(string(proto), prov, model)
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

// ProviderStatus snapshots one provider's resilience knobs.
type ProviderStatus struct {
	Route         string
	Protocol      string
	Credentials   int // failover pool size (0 = single key, no pool)
	Available     int // pool keys not currently benched
	MaxConcurrent int
	InFlight      int // streams currently holding a gate slot
}

// Status snapshots engine and catalog state for observability.
type Status struct {
	Providers            map[string]ProviderStatus
	CatalogLastRefreshAt time.Time
	CatalogLastError     string
}

// Status collects a consistent snapshot of registered providers, failover
// pools, concurrency gates, and the catalog refresh state.
func (e *Engine) Status() Status {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := Status{Providers: map[string]ProviderStatus{}}
	now := e.now()
	for name, conn := range e.providers {
		proto := conn.Protocol
		if proto == "" {
			proto = provider.ProtocolForRoute(conn.Route)
		}
		ps := ProviderStatus{
			Route:         conn.Route,
			Protocol:      string(proto),
			MaxConcurrent: conn.MaxConcurrent,
		}
		if pool := e.pools[name]; pool != nil {
			ps.Credentials = len(pool.creds)
			for _, c := range pool.creds {
				if c.failures < e.failThreshold || !now.Before(c.unavailableUntil) {
					ps.Available++
				}
			}
		}
		if sem := e.sems[name]; sem != nil {
			ps.InFlight = len(sem)
		}
		out.Providers[name] = ps
	}
	if at, err := e.catalog.Status(); !at.IsZero() {
		out.CatalogLastRefreshAt = at
		if err != nil {
			out.CatalogLastError = err.Error()
		}
	}
	return out
}

// Close releases background resources owned by the engine (the catalog's
// refresh loop).
func (e *Engine) Close() error {
	return e.catalog.Close()
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
	proto := conn.Protocol
	if proto == "" {
		proto = provider.ProtocolForRoute(conn.Route)
	}
	if key := e.selectCredential(prov); key != "" {
		conn.APIKey = key
	}
	anthropic := proto == provider.ProtocolAnthropic
	marks := provider.ComputeCacheMarks(req.Cache, req.CacheBreakpoints, len(req.Messages), anthropic)
	conn.BaseURL = provider.ResolveBaseURL(conn)

	if req.Thinking == "off" {
		req.ThinkingOffEffort = e.catalog.ThinkingSpec(string(proto), prov, req.Model.ID).OffEffort
	}

	adapter := e.newAdapter(proto, marks)

	e.mu.RLock()
	sem := e.sems[prov]
	e.mu.RUnlock()
	if sem != nil {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	src, err := adapter.Stream(ctx, conn, req)
	if err != nil {
		if sem != nil {
			<-sem
		}
		e.reportCredential(prov, conn.APIKey, false)
		return nil, err
	}
	e.reportCredential(prov, conn.APIKey, true)
	out := make(chan provider.Event)
	go e.forward(ctx, prov, req.Model.ID, src, out, sem)
	return out, nil
}

// forward copies events through, computing cost on the done event. It stops
// when ctx is cancelled so an abandoned consumer cannot leak the goroutine.
// A non-nil sem slot (the provider concurrency gate) is released on exit.
func (e *Engine) forward(ctx context.Context, prov, model string, src <-chan provider.Event, out chan<- provider.Event, sem chan struct{}) {
	defer close(out)
	if sem != nil {
		defer func() { <-sem }()
	}
	table := e.catalog.PriceTable()
	for {
		var ev provider.Event
		select {
		case e2, ok := <-src:
			if !ok {
				return
			}
			ev = e2
		case <-ctx.Done():
			return
		}
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
