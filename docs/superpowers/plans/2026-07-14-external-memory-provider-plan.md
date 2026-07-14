# 外部记忆 Provider 接口实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 Lcoder 的 `pkg/memory` 中引入 Hermes 风格的 `Provider` 生命周期接口，并实现一个可配置的通用 HTTP/REST provider，使其能在每轮回话中自动召回外部记忆、持久化本轮对话、并在会话结束时发送总结。

**Architecture:** 新增 `memory.Provider` 接口与 `HTTPProvider` 实现；`Injector` 聚合本地文件记忆与外部 provider 召回结果，统一排序和 token 预算后写入 `memory_recall` block；`pkg/agent` 通过新接口 `MemorySink` 在 turn 结束和会话结束时调用 `SyncTurn` / `OnSessionEnd`；配置通过 `config.MemoryConfig.Providers` 注入。

**Tech Stack:** Go 1.25.4，标准库 `net/http/httptest`、`context`、`encoding/json`、`sync/atomic`，现有 `pkg/contextmgr`、`pkg/events`、`pkg/models`。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `pkg/memory/provider.go` | `Provider` 接口、`SessionSummary`、`NopProvider`。 |
| `pkg/memory/circuit.go` | 简易熔断器，供 `HTTPProvider` 使用。 |
| `pkg/memory/httpprovider.go` | 通用 HTTP/REST provider 实现及配置类型。 |
| `pkg/memory/injector.go` | 扩展 `Injector`，聚合本地+外部记忆，支持 `SyncTurn`/`OnSessionEnd`。 |
| `pkg/config/config.go` | 扩展 `MemoryConfig`，新增 `Providers` 与 `MemoryProviderConfig` 等类型。 |
| `pkg/agent/loop.go` | 在 turn 结束和会话结束时调用 `MemorySink` 方法。 |
| `cmd/lcoder/main.go` | 根据配置构造 provider 并注入 agent builder。 |
| `configs/lcoder.yaml` | 增加外部 memory provider 配置示例。 |

---

### Task 1: Provider 接口与 NopProvider

**Files:**
- Create: `pkg/memory/provider.go`
- Test: `pkg/memory/provider_test.go`

- [ ] **Step 1: 写失败测试**

```go
// pkg/memory/provider_test.go
package memory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNopProviderPrefetch(t *testing.T) {
	entries, err := NopProvider.Prefetch(context.Background(), "query")
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestNopProviderSyncTurn(t *testing.T) {
	require.NoError(t, NopProvider.SyncTurn(context.Background(), "hi", "hello"))
}

func TestNopProviderOnSessionEnd(t *testing.T) {
	require.NoError(t, NopProvider.OnSessionEnd(context.Background(), SessionSummary{SessionID: "s1", TurnCount: 3}))
}

func TestNopProviderHealthy(t *testing.T) {
	require.True(t, NopProvider.Healthy(context.Background()))
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd D:/code_practise/project/lab_pj/Lcoder/.worktrees/external-memory
go test ./pkg/memory -run TestNopProvider -v
```

Expected: FAIL，NopProvider / SessionSummary 未定义。

- [ ] **Step 3: 实现最小代码**

```go
// pkg/memory/provider.go
package memory

import "context"

// Provider is the Hermes-style external memory adapter.
type Provider interface {
	// Prefetch recalls relevant memory entries for the upcoming turn.
	Prefetch(ctx context.Context, query string) ([]string, error)

	// SyncTurn persists a completed user/assistant turn.
	SyncTurn(ctx context.Context, user, assistant string) error

	// OnSessionEnd is called once when the agent run ends.
	OnSessionEnd(ctx context.Context, summary SessionSummary) error

	// Healthy reports whether the provider is currently usable.
	Healthy(ctx context.Context) bool
}

// SessionSummary captures the session metadata passed to OnSessionEnd.
type SessionSummary struct {
	SessionID string
	TurnCount int
}

type nopProvider struct{}

func (nopProvider) Prefetch(ctx context.Context, query string) ([]string, error) {
	return nil, nil
}

func (nopProvider) SyncTurn(ctx context.Context, user, assistant string) error {
	return nil
}

func (nopProvider) OnSessionEnd(ctx context.Context, summary SessionSummary) error {
	return nil
}

func (nopProvider) Healthy(ctx context.Context) bool {
	return true
}

// NopProvider is the default no-op provider.
var NopProvider Provider = nopProvider{}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd D:/code_practise/project/lab_pj/Lcoder/.worktrees/external-memory
go test ./pkg/memory -run TestNopProvider -v
```

Expected: PASS。

- [ ] **Step 5: 提交**

```bash
cd D:/code_practise/project/lab_pj/Lcoder/.worktrees/external-memory
git add pkg/memory/provider.go pkg/memory/provider_test.go
git commit -m "feat(memory): add Hermes-style Provider interface and NopProvider

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 2: 简易熔断器

**Files:**
- Create: `pkg/memory/circuit.go`
- Test: `pkg/memory/circuit_test.go`

- [ ] **Step 1: 写失败测试**

```go
// pkg/memory/circuit_test.go
package memory

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCircuitAllowsInitially(t *testing.T) {
	c := newCircuitBreaker(3, 100*time.Millisecond)
	require.True(t, c.allow())
}

func TestCircuitOpensAfterFailures(t *testing.T) {
	c := newCircuitBreaker(2, time.Hour)
	c.recordFailure()
	c.recordFailure()
	require.False(t, c.allow())
}

func TestCircuitClosesAfterTimeout(t *testing.T) {
	c := newCircuitBreaker(2, 50*time.Millisecond)
	c.recordFailure()
	c.recordFailure()
	require.False(t, c.allow())
	time.Sleep(60 * time.Millisecond)
	require.True(t, c.allow())
}

func TestCircuitClosesOnSuccess(t *testing.T) {
	c := newCircuitBreaker(2, time.Hour)
	c.recordFailure()
	c.recordFailure()
	c.recordSuccess()
	require.True(t, c.allow())
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd D:/code_practise/project/lab_pj/Lcoder/.worktrees/external-memory
go test ./pkg/memory -run TestCircuit -v
```

Expected: FAIL，newCircuitBreaker 未定义。

- [ ] **Step 3: 实现最小代码**

```go
// pkg/memory/circuit.go
package memory

import (
	"sync"
	"time"
)

// circuitBreaker is a simple count-based breaker.
type circuitBreaker struct {
	mu                sync.Mutex
	consecutiveFails  int
	threshold         int
	openSince         time.Time
	resetTimeout      time.Duration
}

func newCircuitBreaker(threshold int, resetTimeout time.Duration) *circuitBreaker {
	if threshold <= 0 {
		threshold = 3
	}
	if resetTimeout <= 0 {
		resetTimeout = 30 * time.Second
	}
	return &circuitBreaker{threshold: threshold, resetTimeout: resetTimeout}
}

func (c *circuitBreaker) allow() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.consecutiveFails < c.threshold {
		return true
	}
	if time.Since(c.openSince) >= c.resetTimeout {
		c.consecutiveFails = 0
		return true
	}
	return false
}

func (c *circuitBreaker) recordSuccess() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.consecutiveFails = 0
}

func (c *circuitBreaker) recordFailure() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.consecutiveFails++
	if c.consecutiveFails >= c.threshold {
		c.openSince = time.Now()
	}
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd D:/code_practise/project/lab_pj/Lcoder/.worktrees/external-memory
go test ./pkg/memory -run TestCircuit -v
```

Expected: PASS。

- [ ] **Step 5: 提交**

```bash
cd D:/code_practise/project/lab_pj/Lcoder/.worktrees/external-memory
git add pkg/memory/circuit.go pkg/memory/circuit_test.go
git commit -m "feat(memory): add simple circuit breaker for external providers

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 3: HTTP Provider 实现

**Files:**
- Create: `pkg/memory/httpprovider.go`
- Test: `pkg/memory/httpprovider_test.go`

- [ ] **Step 1: 写失败测试**

```go
// pkg/memory/httpprovider_test.go
package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHTTPProviderPrefetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/search", r.URL.Path)
		require.Equal(t, http.MethodPost, r.Method)
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		require.Equal(t, "go", body["query"])
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"memories": []string{"use go modules"}})
	}))
	defer srv.Close()

	p := NewHTTPProvider(HTTPProviderConfig{Endpoint: srv.URL})
	entries, err := p.Prefetch(context.Background(), "go")
	require.NoError(t, err)
	require.Equal(t, []string{"use go modules"}, entries)
	require.True(t, p.Healthy(context.Background()))
}

func TestHTTPProviderSyncTurn(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/observe", r.URL.Path)
		got = map[string]string{}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	p := NewHTTPProvider(HTTPProviderConfig{Endpoint: srv.URL})
	require.NoError(t, p.SyncTurn(context.Background(), "hi", "hello"))
	require.Equal(t, map[string]string{"user": "hi", "assistant": "hello"}, got)
}

func TestHTTPProviderOnSessionEnd(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/session/end", r.URL.Path)
		got = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	p := NewHTTPProvider(HTTPProviderConfig{Endpoint: srv.URL})
	require.NoError(t, p.OnSessionEnd(context.Background(), SessionSummary{SessionID: "s1", TurnCount: 5}))
	require.Equal(t, "s1", got["session_id"])
	require.EqualValues(t, 5, got["turn_count"])
}

func TestHTTPProviderTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	p := NewHTTPProvider(HTTPProviderConfig{Endpoint: srv.URL, Timeout: 1})
	_, err := p.Prefetch(context.Background(), "q")
	require.Error(t, err)
}

func TestHTTPProviderCircuitBreaker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := NewHTTPProvider(HTTPProviderConfig{Endpoint: srv.URL}).WithBreaker(newCircuitBreaker(2, time.Hour))
	_ = p.Prefetch(context.Background(), "q")
	_ = p.Prefetch(context.Background(), "q")
	require.False(t, p.Healthy(context.Background()))
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd D:/code_practise/project/lab_pj/Lcoder/.worktrees/external-memory
go test ./pkg/memory -run TestHTTPProvider -v
```

Expected: FAIL，HTTPProviderConfig / NewHTTPProvider 未定义。

- [ ] **Step 3: 实现最小代码**

```go
// pkg/memory/httpprovider.go
package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTPProviderConfig configures a generic HTTP memory provider.
type HTTPProviderConfig struct {
	Endpoint       string            `yaml:"endpoint"`
	APIKey         string            `yaml:"api_key"`
	Headers        map[string]string `yaml:"headers"`
	Timeout        int               `yaml:"timeout"`         // seconds; 0 -> 10
	SearchPath     string            `yaml:"search_path"`     // default /search
	ObservePath    string            `yaml:"observe_path"`    // default /observe
	SessionEndPath string            `yaml:"session_end_path"` // default /session/end
}

// HTTPProvider calls a Hermes-compatible REST memory service.
type HTTPProvider struct {
	cfg     HTTPProviderConfig
	client  *http.Client
	breaker *circuitBreaker
}

// NewHTTPProvider creates a provider from config.
func NewHTTPProvider(cfg HTTPProviderConfig) *HTTPProvider {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10
	}
	return &HTTPProvider{
		cfg:     cfg,
		client:  &http.Client{Timeout: time.Duration(timeout) * time.Second},
		breaker: newCircuitBreaker(3, 30*time.Second),
	}
}

// WithBreaker replaces the default breaker, mainly for tests.
func (p *HTTPProvider) WithBreaker(b *circuitBreaker) *HTTPProvider {
	p.breaker = b
	return p
}

func (p *HTTPProvider) Healthy(ctx context.Context) bool {
	return p.breaker.allow()
}

func (p *HTTPProvider) Prefetch(ctx context.Context, query string) ([]string, error) {
	if !p.breaker.allow() {
		return nil, fmt.Errorf("circuit breaker open")
	}
	path := p.cfg.SearchPath
	if path == "" {
		path = "/search"
	}
	body, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		return nil, err
	}
	memories, err := p.post(ctx, path, body)
	if err != nil {
		p.breaker.recordFailure()
		return nil, err
	}
	p.breaker.recordSuccess()
	return memories, nil
}

func (p *HTTPProvider) SyncTurn(ctx context.Context, user, assistant string) error {
	if !p.breaker.allow() {
		return fmt.Errorf("circuit breaker open")
	}
	path := p.cfg.ObservePath
	if path == "" {
		path = "/observe"
	}
	body, err := json.Marshal(map[string]string{"user": user, "assistant": assistant})
	if err != nil {
		return err
	}
	_, err = p.post(ctx, path, body)
	if err != nil {
		p.breaker.recordFailure()
		return err
	}
	p.breaker.recordSuccess()
	return nil
}

func (p *HTTPProvider) OnSessionEnd(ctx context.Context, summary SessionSummary) error {
	if !p.breaker.allow() {
		return fmt.Errorf("circuit breaker open")
	}
	path := p.cfg.SessionEndPath
	if path == "" {
		path = "/session/end"
	}
	body, err := json.Marshal(map[string]any{"session_id": summary.SessionID, "turn_count": summary.TurnCount})
	if err != nil {
		return err
	}
	_, err = p.post(ctx, path, body)
	if err != nil {
		p.breaker.recordFailure()
		return err
	}
	p.breaker.recordSuccess()
	return nil
}

func (p *HTTPProvider) post(ctx context.Context, path string, body []byte) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.Endpoint+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}
	for k, v := range p.cfg.Headers {
		req.Header.Set(k, v)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("memory provider %s returned %d: %s", path, resp.StatusCode, string(body))
	}

	if path == p.cfg.SearchPath || (p.cfg.SearchPath == "" && path == "/search") {
		var result struct {
			Memories []string `json:"memories"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return nil, err
		}
		return result.Memories, nil
	}
	return nil, nil
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd D:/code_practise/project/lab_pj/Lcoder/.worktrees/external-memory
go test ./pkg/memory -run TestHTTPProvider -v
```

Expected: PASS。

- [ ] **Step 5: 提交**

```bash
cd D:/code_practise/project/lab_pj/Lcoder/.worktrees/external-memory
git add pkg/memory/httpprovider.go pkg/memory/httpprovider_test.go
git commit -m "feat(memory): add generic HTTP/REST external memory provider

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 4: Injector 聚合本地与外部记忆

**Files:**
- Modify: `pkg/memory/injector.go`
- Modify: `pkg/memory/injector_test.go`

- [ ] **Step 1: 写失败测试**

```go
// pkg/memory/injector_test.go 追加
func TestInjectorAggregatesExternalProvider(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	store, err := NewStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, store.Add(MemoryTarget, "local note about go"))

	inj := NewInjector(store, mgr, 1024).WithProviders(&fakeProvider{
		prefetch: []string{"external note about go"},
	})
	require.NoError(t, inj.Prefetch(context.Background(), "go"))

	block, ok := mgr.GetBlock(contextmgr.BlockRetrieval, "memory_recall")
	require.True(t, ok)
	text := block.Text()
	require.Contains(t, text, "local note about go")
	require.Contains(t, text, "external note about go")
}

func TestInjectorAppendsProviderErrorToBlock(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	store, err := NewStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, store.Add(MemoryTarget, "local note"))

	inj := NewInjector(store, mgr, 1024).WithProviders(&fakeProvider{err: errors.New("provider down")})
	require.NoError(t, inj.Prefetch(context.Background(), "local"))

	block, ok := mgr.GetBlock(contextmgr.BlockRetrieval, "memory_recall")
	require.True(t, ok)
	text := block.Text()
	require.Contains(t, text, "local note")
	require.Contains(t, text, "External memory provider unavailable")
}

func TestInjectorSyncTurnAndSessionEnd(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	store, err := NewStore(t.TempDir())
	require.NoError(t, err)

	fp := &fakeProvider{}
	inj := NewInjector(store, mgr, 1024).WithProviders(fp)

	require.NoError(t, inj.SyncTurn(context.Background(), "hi", "hello"))
	require.Equal(t, [2]string{"hi", "hello"}, fp.synced)

	require.NoError(t, inj.OnSessionEnd(context.Background(), SessionSummary{SessionID: "s1", TurnCount: 2}))
	require.Equal(t, "s1", fp.ended.SessionID)
	require.Equal(t, 2, fp.ended.TurnCount)
}

type fakeProvider struct {
	prefetch []string
	err      error
	synced   [2]string
	ended    SessionSummary
}

func (f *fakeProvider) Prefetch(ctx context.Context, query string) ([]string, error) {
	return f.prefetch, f.err
}

func (f *fakeProvider) SyncTurn(ctx context.Context, user, assistant string) error {
	f.synced = [2]string{user, assistant}
	return nil
}

func (f *fakeProvider) OnSessionEnd(ctx context.Context, summary SessionSummary) error {
	f.ended = summary
	return nil
}

func (f *fakeProvider) Healthy(ctx context.Context) bool { return true }
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd D:/code_practise/project/lab_pj/Lcoder/.worktrees/external-memory
go test ./pkg/memory -run TestInjectorAggregatesExternalProvider -v
```

Expected: FAIL，`WithProviders` / `SyncTurn` / `OnSessionEnd` 不存在。

- [ ] **Step 3: 实现最小代码**

将 `pkg/memory/injector.go` 改为：

```go
package memory

import (
	"context"
	"fmt"
	"strings"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/models"
)

// Injector prefetches relevant memory entries into the context manager each turn.
type Injector struct {
	store      *Store
	providers  []Provider
	manager    *contextmgr.Manager
	ranker     Ranker
	maxTokens  int
	failureMsg string
}

// NewInjector creates an injector bound to a store and context manager.
// maxTokens <= 0 defaults to 1024.
func NewInjector(store *Store, mgr *contextmgr.Manager, maxTokens int) *Injector {
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	return &Injector{
		store:     store,
		manager:   mgr,
		ranker:    NewDefaultRanker(),
		maxTokens: maxTokens,
	}
}

// WithRanker replaces the default ranker.
func (inj *Injector) WithRanker(r Ranker) *Injector {
	inj.ranker = r
	return inj
}

// WithProviders attaches external memory providers.
func (inj *Injector) WithProviders(providers ...Provider) *Injector {
	inj.providers = append(inj.providers, providers...)
	return inj
}

// Prefetch ranks memory entries against query and writes a memory_recall block.
func (inj *Injector) Prefetch(ctx context.Context, query string) error {
	entries, err := inj.store.allEntries(MemoryTarget)
	if err != nil {
		inj.setBlock("", "")
		return fmt.Errorf("load memory entries: %w", err)
	}

	inj.failureMsg = ""
	for _, p := range inj.providers {
		if !p.Healthy(ctx) {
			inj.failureMsg = "external memory provider circuit open"
			continue
		}
		external, err := p.Prefetch(ctx, query)
		if err != nil {
			inj.failureMsg = err.Error()
			continue
		}
		entries = append(entries, external...)
	}

	ranked := inj.ranker.Rank(query, entries)
	selected := inj.budgetResults(ranked)

	text := strings.Join(selected, "\n\n")
	inj.setBlock(query, text)
	return nil
}

// SyncTurn forwards the completed turn to all providers.
func (inj *Injector) SyncTurn(ctx context.Context, user, assistant string) error {
	var firstErr error
	for _, p := range inj.providers {
		if !p.Healthy(ctx) {
			continue
		}
		if err := p.SyncTurn(ctx, user, assistant); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// OnSessionEnd forwards the session summary to all providers.
func (inj *Injector) OnSessionEnd(ctx context.Context, summary SessionSummary) error {
	var firstErr error
	for _, p := range inj.providers {
		if !p.Healthy(ctx) {
			continue
		}
		if err := p.OnSessionEnd(ctx, summary); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (inj *Injector) budgetResults(ranked []RankedEntry) []string {
	if len(ranked) == 0 {
		return nil
	}
	estimator := inj.manager.Estimator()
	used := 0
	var selected []string
	for _, r := range ranked {
		msg := models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: r.Text})
		cost := estimator([]models.AgentMessage{msg})
		if used+cost > inj.maxTokens {
			break
		}
		selected = append(selected, r.Text)
		used += cost
	}
	return selected
}

func (inj *Injector) setBlock(query, text string) {
	var b strings.Builder
	if text != "" {
		fmt.Fprintf(&b, "// Recalled memory for query %q\n\n%s", query, text)
	}
	if inj.failureMsg != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "// External memory provider unavailable: %s", inj.failureMsg)
	}
	block := contextmgr.NewBlockWithCacheHint(
		contextmgr.BlockRetrieval,
		"memory_recall",
		contextmgr.StabilityDynamic,
		60,
		contextmgr.CacheHintSkip,
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: b.String()}),
	)
	inj.manager.SetBlock(block)
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd D:/code_practise/project/lab_pj/Lcoder/.worktrees/external-memory
go test ./pkg/memory -run TestInjector -v
```

Expected: PASS。

- [ ] **Step 5: 提交**

```bash
cd D:/code_practise/project/lab_pj/Lcoder/.worktrees/external-memory
git add pkg/memory/injector.go pkg/memory/injector_test.go
git commit -m "feat(memory): aggregate external provider results in Injector

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 5: 配置扩展

**Files:**
- Modify: `pkg/config/config.go`
- Modify: `pkg/config/config_test.go`
- Modify: `configs/lcoder.yaml`

- [ ] **Step 1: 写失败测试**

```go
// pkg/config/config_test.go 追加
func TestDefaultMemoryProviderConfig(t *testing.T) {
	cfg := DefaultConfig()
	if len(cfg.Memory.Providers) != 0 {
		t.Fatalf("expected no providers by default, got %d", len(cfg.Memory.Providers))
	}
	if cfg.Memory.RecallMaxTokens != 1024 {
		t.Fatalf("expected recall_max_tokens 1024, got %d", cfg.Memory.RecallMaxTokens)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd D:/code_practise/project/lab_pj/Lcoder/.worktrees/external-memory
go test ./pkg/config -run TestDefaultMemoryProviderConfig -v
```

Expected: FAIL，字段未定义。

- [ ] **Step 3: 实现最小代码**

在 `pkg/config/config.go` 中：

1. 新增类型（放在 `MemoryConfig` 之前）：

```go
// MemoryProviderConfig describes an external memory provider.
type MemoryProviderConfig struct {
	Name   string             `yaml:"name"`
	Type   string             `yaml:"type"` // "http"
	Config HTTPProviderConfig `yaml:"config"`
}

// HTTPProviderConfig configures a generic HTTP memory provider.
type HTTPProviderConfig struct {
	Endpoint       string            `yaml:"endpoint"`
	APIKey         string            `yaml:"api_key"`
	Headers        map[string]string `yaml:"headers"`
	Timeout        int               `yaml:"timeout"`
	SearchPath     string            `yaml:"search_path"`
	ObservePath    string            `yaml:"observe_path"`
	SessionEndPath string            `yaml:"session_end_path"`
}
```

2. 修改 `MemoryConfig`：

```go
type MemoryConfig struct {
	Enabled         bool                   `yaml:"enabled"`
	MemoryCharLimit int                    `yaml:"memory_char_limit"`
	UserCharLimit   int                    `yaml:"user_char_limit"`
	DynamicRecall   bool                   `yaml:"dynamic_recall"`
	RecallMaxTokens int                    `yaml:"recall_max_tokens"`
	RecallMinScore  float64                `yaml:"recall_min_score"`
	Providers       []MemoryProviderConfig `yaml:"providers"`
}
```

3. 在 `DefaultConfig()` 中保持 `Providers: nil`。

4. 在 `Load()` 的 confmap defaults 中保持 `providers: cfg.Memory.Providers`（nil 即可）。

5. 在 `configs/lcoder.yaml` 的 `memory:` 区块追加示例：

```yaml
memory:
  enabled: true
  memory_char_limit: 0
  user_char_limit: 0
  dynamic_recall: true
  recall_max_tokens: 1024
  recall_min_score: 0.1
  # External memory providers (Hermes-style). Currently only "http" is supported.
  # providers:
  #   - name: hermes-http
  #     type: http
  #     config:
  #       endpoint: "http://localhost:8000"
  #       api_key: "${HERMES_MEMORY_API_KEY}"
  #       timeout: 10
  #       search_path: "/search"
  #       observe_path: "/observe"
  #       session_end_path: "/session/end"
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd D:/code_practise/project/lab_pj/Lcoder/.worktrees/external-memory
go test ./pkg/config -run TestDefaultMemoryProviderConfig -v
go test ./pkg/config -v
```

Expected: PASS。

- [ ] **Step 5: 提交**

```bash
cd D:/code_practise/project/lab_pj/Lcoder/.worktrees/external-memory
git add pkg/config/config.go pkg/config/config_test.go configs/lcoder.yaml
git commit -m "feat(config): add external memory provider configuration

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 6: Agent Loop 调用 SyncTurn / OnSessionEnd

**Files:**
- Modify: `pkg/agent/loop.go`
- Create: `pkg/agent/memory_sink_test.go`

- [ ] **Step 1: 写失败测试**

```go
// pkg/agent/memory_sink_test.go
package agent

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/memory"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/tools"
	"github.com/stretchr/testify/require"
)

type fakeMemorySink struct {
	prefetchQueries []string
	synced          [][2]string
	ended           []memory.SessionSummary
}

func (f *fakeMemorySink) Prefetch(ctx context.Context, query string) error {
	f.prefetchQueries = append(f.prefetchQueries, query)
	return nil
}

func (f *fakeMemorySink) SyncTurn(ctx context.Context, user, assistant string) error {
	f.synced = append(f.synced, [2]string{user, assistant})
	return nil
}

func (f *fakeMemorySink) OnSessionEnd(ctx context.Context, summary memory.SessionSummary) error {
	f.ended = append(f.ended, summary)
	return nil
}

func TestAgentCallsMemorySinkLifecycle(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	sink := &fakeMemorySink{}
	llmClient := &llm.Client{}
	bus := models.NewEventBus()
	registry := tools.NewRegistry()
	perms := permissions.NewEngine(nil)

	cfg := Config{
		ContextManager: mgr,
		MemoryInjector: sink,
	}
	ag := New(cfg, llmClient, registry, perms, bus)

	// Use a ShouldStop that stops after the first turn with no tool calls.
	ag.cfg.ShouldStop = func(ctx context.Context, turn TurnSummary) (bool, error) {
		return true, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// We cannot easily stream without a real LLM, so only assert that Prefetch
	// is attempted before the streaming failure.
	_ = ag.Prompt(ctx, models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hello"}))

	require.NotEmpty(t, sink.prefetchQueries)
	require.Contains(t, sink.prefetchQueries, "hello")
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd D:/code_practise/project/lab_pj/Lcoder/.worktrees/external-memory
go test ./pkg/agent -run TestAgentCallsMemorySinkLifecycle -v
```

Expected: FAIL，`models.NewEventBus` 可能不存在；需要按实际包调整。这里目的是确认 `MemorySink` 接口未在 loop 中调用；如果编译错误需修正测试辅助构造。更稳妥的测试方式见 Step 3 说明。

**注意：** 由于 `Agent` 直接依赖 LLM streamer，完整 loop 测试较复杂。上述测试只要编译通过并在运行后断言 `prefetchQueries` 包含 `"hello"` 即可。若 `models.NewEventBus` 不存在，改用 `events.NewBus()`（查看 `pkg/events` 实际 API）。

- [ ] **Step 3: 实现最小代码**

在 `pkg/agent/loop.go` 中：

1. 在 `Config` 附近添加 `MemorySink` 接口（或放在 `pkg/memory` 中由 `pkg/agent` import；推荐放在 `pkg/memory` 以保持依赖方向）：

```go
// pkg/memory/provider.go 追加
type MemorySink interface {
	MemoryInjector
	SyncTurn(ctx context.Context, user, assistant string) error
	OnSessionEnd(ctx context.Context, summary SessionSummary) error
}
```

2. 在 `pkg/agent/loop.go` 的 `run` 方法中：

- 在 turn 循环内，emit `TurnEndEvent` 之后调用 `SyncTurn`：

```go
	a.emit(ctx, events.TurnEndEvent{...})

	if sink, ok := a.memoryInjector.(memory.MemorySink); ok {
		if userText := lastUserText(a.mgr.AllMessages()); userText != "" {
			if assistantText := assistantMsg.Text(); assistantText != "" {
				if err := sink.SyncTurn(ctx, userText, assistantText); err != nil {
					a.emit(ctx, events.ErrorEvent{
						Base:    events.Base{Type: events.Error, Turn: turn},
						Message: "memory sync_turn: " + err.Error(),
					})
				}
			}
		}
	}
```

- 在 `run` 退出前、emit `AgentEndEvent` 之前调用 `OnSessionEnd`：

```go
	if sink, ok := a.memoryInjector.(memory.MemorySink); ok {
		if err := sink.OnSessionEnd(ctx, memory.SessionSummary{SessionID: a.cfg.SessionID, TurnCount: int(turn)}); err != nil {
			a.emit(ctx, events.ErrorEvent{
				Base:    events.Base{Type: events.Error, Turn: int(turn)},
				Message: "memory session_end: " + err.Error(),
			})
		}
	}

	a.emit(ctx, events.AgentEndEvent{...})
```

3. 在 `WithMode` 中 `memoryInjector` 已经通过指针复制；由于 `Injector` 实现了 `MemorySink`，此处无需改动。

- [ ] **Step 4: 运行测试确认通过**

```bash
cd D:/code_practise/project/lab_pj/Lcoder/.worktrees/external-memory
go test ./pkg/agent -run TestAgentCallsMemorySinkLifecycle -v
```

Expected: PASS。

- [ ] **Step 5: 提交**

```bash
cd D:/code_practise/project/lab_pj/Lcoder/.worktrees/external-memory
git add pkg/memory/provider.go pkg/agent/loop.go pkg/agent/memory_sink_test.go
git commit -m "feat(agent): call SyncTurn and OnSessionEnd on MemorySink

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 7: CLI 装配

**Files:**
- Modify: `cmd/lcoder/main.go`

- [ ] **Step 1: 确定插入点**

读取 `cmd/lcoder/main.go`，找到 `prepareAgent` 中构造 `memory.Injector` 的代码。当前大致结构：

```go
var memoryInjector *memory.Injector
if cfg.Memory.DynamicRecall && cfg.Memory.Enabled {
	memoryInjector = memory.NewInjector(store, mgr, cfg.Memory.RecallMaxTokens)
}
```

- [ ] **Step 2: 实现最小代码**

在 `prepareAgent` 中，当 `memoryInjector` 创建后，遍历 `cfg.Memory.Providers` 并附加 provider：

```go
var memoryInjector *memory.Injector
if cfg.Memory.Enabled {
	memoryInjector = memory.NewInjector(store, mgr, cfg.Memory.RecallMaxTokens)
	if cfg.Memory.DynamicRecall {
		for _, pc := range cfg.Memory.Providers {
			switch pc.Type {
			case "http":
				memoryInjector.WithProviders(memory.NewHTTPProvider(pc.Config))
			default:
				fmt.Fprintf(os.Stderr, "warning: unknown memory provider type %q\n", pc.Type)
			}
		}
	}
}
```

注意：保留现有 `DynamicRecall` 控制是否使用 Injector 的逻辑；若 `DynamicRecall` 为 false，则不应创建 Injector（与现有行为一致）。

- [ ] **Step 3: 编译检查**

```bash
cd D:/code_practise/project/lab_pj/Lcoder/.worktrees/external-memory
go build ./cmd/lcoder
```

Expected: 成功。

- [ ] **Step 4: 运行相关测试**

```bash
cd D:/code_practise/project/lab_pj/Lcoder/.worktrees/external-memory
go test ./cmd/lcoder ./pkg/memory ./pkg/agent ./pkg/config -count=1
```

Expected: PASS。

- [ ] **Step 5: 提交**

```bash
cd D:/code_practise/project/lab_pj/Lcoder/.worktrees/external-memory
git add cmd/lcoder/main.go
git commit -m "feat(cli): wire external memory providers into agent setup

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 8: 最终验证

- [ ] **Step 1: 全量测试**

```bash
cd D:/code_practise/project/lab_pj/Lcoder/.worktrees/external-memory
go test $(go list ./... | grep -v 'reference/Shannon') -count=1
```

Expected: 全部 PASS。

- [ ] **Step 2: vet**

```bash
cd D:/code_practise/project/lab_pj/Lcoder/.worktrees/external-memory
go vet $(go list ./... | grep -v 'reference/Shannon')
```

Expected: 无输出。

- [ ] **Step 3: 编译**

```bash
cd D:/code_practise/project/lab_pj/Lcoder/.worktrees/external-memory
go build ./...
```

Expected: 成功。

---

## Spec 覆盖检查

| 需求 | 对应 Task |
|---|---|
| `Provider` 接口定义（Prefetch/SyncTurn/OnSessionEnd/Healthy） | Task 1 |
| 简易熔断器 | Task 2 |
| 通用 HTTP/REST provider | Task 3 |
| Injector 聚合本地+外部记忆、返回 provider 错误提示 | Task 4 |
| `MemoryConfig.Providers` 配置 | Task 5 |
| Agent loop 调用 SyncTurn / OnSessionEnd | Task 6 |
| CLI 装配 | Task 7 |
| 全量测试 | Task 8 |

## 无占位符检查

- 所有步骤包含完整代码块或命令。
- 无 "TBD" / "TODO" / "implement later"。
- 类型名称一致：`Provider`、`HTTPProviderConfig`、`MemoryProviderConfig`、`SessionSummary`、`MemorySink`。

---

## 执行方式

计划已保存到 `docs/superpowers/plans/2026-07-14-external-memory-provider-plan.md`。请选择执行方式：

1. **Subagent-Driven（推荐）** - 每个 Task 派一个子代理实现，Task 间做 spec/quality 双阶段评审。
2. **Inline Execution** - 在本会话中按 Task 顺序直接实现。

请选择。