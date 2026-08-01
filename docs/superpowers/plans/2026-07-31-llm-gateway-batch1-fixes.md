# LLM Gateway 第一批缺陷修复 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复 pkg/llm 六个已确认的实现缺陷：429/5xx 重试失效、engine.forward goroutine 泄漏、Anthropic 流内 error 被吞、OpenAI 缺 include_usage、providers map 数据竞争、catalog 别名大小写失效。

**Architecture:** 不改任何对外接口形状（`Adapter.Stream` 签名不变，但语义收紧：HTTP 非 200 在建流阶段同步返回 error)。每个缺陷独立一个 task,独立可测、独立提交。

**Tech Stack:** Go 1.25,httptest 驱动的单元测试，`-race` 验证并发修复。

**测试命令约定：** 本仓库从模块根跑 `go test ./pkg/llm/...` 即可（reference/Shannon 排除规则不影响 pkg/llm)。

---

### Task 1: HTTP 状态码同步检查（修复 429/5xx 永不重试）

**根因：** 三个 adapter 把 `resp.StatusCode != 200` 的检查放在返回 channel 之后的 goroutine 里，`adapter.Stream` 对 429/401/500 永远返回 `err == nil`，于是 `pkg/llm/retry.go` 的 `retryableCode{"rate_limit","internal"}` 成为死代码。

**Files:**
- Modify: `pkg/llm/provider/adapter.go`（加共享 helper)
- Modify: `pkg/llm/provider/anthropic.go:103-119`
- Modify: `pkg/llm/provider/openai.go:78-94`
- Modify: `pkg/llm/provider/openai_responses.go:66-82`
- Test: `pkg/llm/provider/openai_test.go:246-263`（改写既有测试）
- Test: `pkg/llm/retry_test.go`（新增端到端重试测试）

- [ ] **Step 1: 改写 `TestOpenAIStreamHTTPErrorClassified` 为同步 error 语义（失败测试）**

`pkg/llm/provider/openai_test.go` 中该测试整体替换为：

```go
func TestOpenAIStreamHTTPErrorClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
	}))
	t.Cleanup(srv.Close)
	ad := OpenAICompat{}
	_, err := ad.Stream(context.Background(), Conn{BaseURL: srv.URL, Route: "openai"},
		models.TurnRequest{Model: models.ModelRef{Provider: "openai", ID: "gpt-4o"}})
	var pe *EventError
	if !errors.As(err, &pe) || pe.Code != "rate_limit" {
		t.Fatalf("want rate_limit EventError from Stream, got %v", err)
	}
}
```

需要在 openai_test.go 的 import 中加 `"errors"`。

- [ ] **Step 2: 新增 429 端到端重试测试（失败测试）**

`pkg/llm/retry_test.go` 追加（package llm，需要新增 import:`net/http`、`net/http/httptest`、`sync/atomic`、`github.com/lcoder/lcoder/pkg/llm/catalog`、`github.com/lcoder/lcoder/pkg/llm/engine`、`github.com/lcoder/lcoder/pkg/llm/provider`):

```go
func TestStreamTurnRetryRetriesOn429(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)

	eng := engine.New(catalog.New(catalog.Options{Refresh: false}))
	eng.RegisterProvider("openai", provider.Conn{BaseURL: srv.URL, Route: "openai"})
	c := NewClient(eng)
	stream, err := c.StreamTurnRetry(context.Background(), models.TurnRequest{
		Model: models.ModelRef{Provider: "openai", ID: "gpt-4o"},
	}, RetryConfig{MaxAttempts: 3, BaseBackoff: time.Millisecond})
	if err != nil {
		t.Fatalf("retry should recover from 429: %v", err)
	}
	for range stream {
	}
	if calls.Load() != 2 {
		t.Fatalf("want 2 attempts, got %d", calls.Load())
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `go test ./pkg/llm/... -run 'TestOpenAIStreamHTTPErrorClassified|TestStreamTurnRetryRetriesOn429' -v`
Expected: 两个都 FAIL —— 前者因为 `Stream` 返回 nil error，后者因为只打了 1 次请求就返回 channel（错误经 channel 传递，StreamTurnRetry 看不到）。

- [ ] **Step 4: 在 adapter.go 加共享 helper**

`pkg/llm/provider/adapter.go` import 增加 `"io"` 和 `"net/http"`，追加：

```go
// maxErrorBodyBytes bounds how much of a failed response body is read into the
// classified error (a proxy may answer with a large HTML page).
const maxErrorBodyBytes = 64 * 1024

// doStreamRequest sends req and returns the response only on 200 OK. Any other
// status is classified and returned synchronously — before the event channel
// exists — so StreamTurnRetry can see rate_limit/internal failures and retry
// them. The body is drained (bounded) and closed on failure.
func doStreamRequest(client *http.Client, req *http.Request) (*http.Response, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		_ = resp.Body.Close()
		return nil, classifyHTTP(resp.StatusCode, data)
	}
	return resp, nil
}
```

- [ ] **Step 5: 三个 adapter 改用 helper**

`anthropic.go`：把 103-119 行

```go
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, err
	}

	out := make(chan Event, 16)
	go func() {
		defer close(out)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			data, _ := io.ReadAll(resp.Body)
			emit(ctx, out, Event{Kind: KindError, Err: classifyHTTP(resp.StatusCode, data)})
			return
		}

		emit(ctx, out, Event{Kind: KindStart})
```

替换为：

```go
	resp, err := doStreamRequest(http.DefaultClient, httpReq)
	if err != nil {
		return nil, err
	}

	out := make(chan Event, 16)
	go func() {
		defer close(out)
		defer resp.Body.Close()

		emit(ctx, out, Event{Kind: KindStart})
```

`openai.go`(78-94 行）与 `openai_responses.go`(66-82 行）做同样的替换。三个文件的 `"io"` import 若不再使用则删除（anthropic.go、openai.go、openai_responses.go 中 `io` 均只用于这处 ReadAll)。

- [ ] **Step 6: 跑测试确认通过 + 全量回归**

Run: `go test ./pkg/llm/... -count=1`
Expected: PASS（含两个新测试与既有全部测试）

- [ ] **Step 7: Commit**

```bash
git add pkg/llm/provider/adapter.go pkg/llm/provider/anthropic.go pkg/llm/provider/openai.go pkg/llm/provider/openai_responses.go pkg/llm/provider/openai_test.go pkg/llm/retry_test.go
git commit -m "fix(llm): classify HTTP errors synchronously so 429/5xx are retryable"
```

---

### Task 2: engine.forward 响应 ctx 取消（修复 goroutine 泄漏)

**根因：** `engine.go:140` 的 `forward` 循环里 `out <- ev` 是无缓冲发送且不监听 ctx;consumer(streamer）取消后直接 return,forward 永久阻塞，每个被中断的 turn 泄漏一个 goroutine。

**Files:**
- Modify: `pkg/llm/engine/engine.go:115-158`
- Test: `pkg/llm/engine/engine_test.go`

- [ ] **Step 1: 写失败测试**

`pkg/llm/engine/engine_test.go` 追加（import 增加 `"time"`):

```go
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
	eng.SetAdapterFactory(func(route string, marks provider.CacheMarks) provider.Adapter {
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
	// Cancelling must unblock it, and the defer then closes out.
	cancel()
	select {
	case _, ok := <-out:
		if ok {
			t.Fatal("expected closed channel after cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("forward goroutine still blocked after cancel (leak)")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./pkg/llm/engine -run TestStreamTurnForwardStopsOnCancel -v`
Expected: FAIL with "forward goroutine still blocked after cancel (leak)"

- [ ] **Step 3: 实现修复**

`engine.go` 的 `StreamTurn` 中 `go e.forward(prov, req.Model.ID, src, out)` 改为 `go e.forward(ctx, prov, req.Model.ID, src, out)`;`forward` 整体替换为：

```go
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
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./pkg/llm/engine -count=1 -v`
Expected: PASS（含既有 TestEngineFillsCostOnDone 等）

- [ ] **Step 5: Commit**

```bash
git add pkg/llm/engine/engine.go pkg/llm/engine/engine_test.go
git commit -m "fix(llm/engine): stop forward goroutine when turn context is cancelled"
```

---

### Task 3: Anthropic 流内 error 事件不再被吞

**根因：** `anthropic.go` 的 switch 只处理 message_start/content_block_*/message_delta/message_stop;Anthropic 的 `{"type":"error","error":{"type":"overloaded_error",...}}` 无任何 case 命中，随后 EOF 发出 KindDone + 空消息，过载被上报为"成功的空回复"。

**Files:**
- Modify: `pkg/llm/provider/anthropic.go:136-171`（事件 switch）和 `:416-434`(anthropicEvent 结构）
- Test: `pkg/llm/provider/anthropic_test.go`

- [ ] **Step 1: 写失败测试**

`pkg/llm/provider/anthropic_test.go` 追加（复用同包 openai_test.go 里的 `sseServer`/`collect`):

```go
func TestAnthropicStreamErrorEvent(t *testing.T) {
	body := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"
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
```

（若 anthropic_test.go 尚无 `"strings"` import 则加上。）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./pkg/llm/provider -run TestAnthropicStreamErrorEvent -v`
Expected: FAIL with "stream error must not be reported as done"（当前 error 帧被跳过，EOF 发出 KindDone)

- [ ] **Step 3: 实现修复**

`anthropicEvent` 结构体（anthropic.go:416-434）在 `Usage` 字段前加：

```go
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
```

事件 switch(anthropic.go:165-171）在 `case "message_stop":` 之后加：

```go
			case "error":
				code := "internal"
				msg := "anthropic stream error"
				if ev.Error != nil {
					// overloaded_error / rate_limit_error are transient and
					// worth an upstream retry; anything else is not.
					if ev.Error.Type == "overloaded_error" || ev.Error.Type == "rate_limit_error" {
						code = "rate_limit"
					}
					if ev.Error.Message != "" {
						msg = ev.Error.Message
					} else if ev.Error.Type != "" {
						msg = ev.Error.Type
					}
				}
				emit(ctx, out, Event{Kind: KindError, Err: &EventError{Code: code, Message: msg}})
				return
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./pkg/llm/provider -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/llm/provider/anthropic.go pkg/llm/provider/anthropic_test.go
git commit -m "fix(llm/provider): surface anthropic in-stream error frames as KindError"
```

---

### Task 4: OpenAI 请求注入 stream_options.include_usage

**根因：** OpenAI 官方流式接口只有显式设置 `stream_options.include_usage` 才在末尾发 usage chunk；不发则 usage/cost 恒为 0,`RecordRealUsage` 反馈链失效。higress 的 ai-proxy 同样强制注入该字段。

**Files:**
- Modify: `pkg/llm/provider/openai.go:30-34`
- Test: `pkg/llm/provider/openai_test.go`

- [ ] **Step 1: 写失败测试**

`pkg/llm/provider/openai_test.go` 追加（复用同包 `captureRequestBody`，见 anthropic_test.go:137 的用法）:

```go
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
```

（先确认 `captureRequestBody` 的签名——anthropic_test.go 中以 `(t, adapter, req)` 调用；若它定义在 anthropic_test.go 且对 OpenAICompat 同样适用则直接用。)

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./pkg/llm/provider -run TestOpenAISendsIncludeUsage -v`
Expected: FAIL with "stream_options.include_usage missing"

- [ ] **Step 3: 实现修复**

`openai.go` 的 body 字面量（30-34 行）改为：

```go
	body := map[string]any{
		"model":    req.Model.ID,
		"messages": withSystem(req.SystemPrompt, openAIMessages(req.Messages)),
		"stream":   true,
		// Ask for the trailing usage chunk; without it OpenAI sends no usage
		// and cost accounting / RecordRealUsage silently see zero.
		"stream_options": map[string]any{"include_usage": true},
	}
```

- [ ] **Step 4: 跑测试确认通过 + 全量回归**

Run: `go test ./pkg/llm/... -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/llm/provider/openai.go pkg/llm/provider/openai_test.go
git commit -m "fix(llm/provider): request include_usage on openai streams"
```

---

### Task 5: engine.providers 加锁（修复 data race)

**根因：** `RegisterProvider` 写 map 与 `StreamTurn`/`ResolveThinking` 读 map 无同步；TUI 热注册 provider(pkg/tui/providerpanel.go）与进行中的 turn 并发，`fatal error: concurrent map read and map write` 可使进程崩溃。

**Files:**
- Modify: `pkg/llm/engine/engine.go:20-24, 50-52, 77, 117`
- Test: `pkg/llm/engine/engine_test.go`

- [ ] **Step 1: 写竞争测试（在 -race 下失败）**

`pkg/llm/engine/engine_test.go` 追加（import 增加 `"sync"`):

```go
func TestProviderRegistrationConcurrentWithStream(t *testing.T) {
	cat := catalog.New(catalog.Options{Refresh: false})
	eng := New(cat)
	eng.SetAdapterFactory(func(route string, marks provider.CacheMarks) provider.Adapter {
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
```

- [ ] **Step 2: 跑测试确认 race**

Run: `go test ./pkg/llm/engine -run TestProviderRegistrationConcurrentWithStream -race -count=1`
Expected: FAIL with "WARNING: DATA RACE"

- [ ] **Step 3: 实现修复**

`engine.go` 的 Engine 结构体加互斥锁：

```go
// Engine routes turns to provider adapters in-process.
type Engine struct {
	mu         sync.RWMutex // guards providers
	providers  map[string]provider.Conn
	catalog    *catalog.Catalog
	newAdapter AdapterFactory
}
```

（import 加 `"sync"`。）三处改动：

```go
// RegisterProvider stores or replaces an in-memory provider connection.
func (e *Engine) RegisterProvider(name string, conn provider.Conn) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.providers[name] = conn
}
```

`ResolveThinking`(engine.go:77):

```go
	e.mu.RLock()
	route := e.providers[provider].Route
	e.mu.RUnlock()
```

`StreamTurn`(engine.go:117):

```go
	e.mu.RLock()
	conn := e.providers[prov]
	e.mu.RUnlock()
```

（`conn` 是值类型，拷出后解锁即可，后续使用安全。)

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./pkg/llm/engine -race -count=1`
Expected: PASS,no data race warning

- [ ] **Step 5: Commit**

```bash
git add pkg/llm/engine/engine.go pkg/llm/engine/engine_test.go
git commit -m "fix(llm/engine): guard providers map against concurrent registration"
```

---

### Task 6: catalog providerAliases 大小写修复

**根因：** `catalog.go:34` 的 `"Zai": "zhipuai"` 键永远不会被命中——全代码库 provider 名均为小写，`providerCandidates` 用传入名做精确 map 查找。

**Files:**
- Modify: `pkg/llm/catalog/catalog.go:31-45`
- Test: `pkg/llm/catalog/catalog_test.go`

- [ ] **Step 1: 写失败测试**

`pkg/llm/catalog/catalog_test.go` 追加：

```go
func TestProviderCandidatesAliasCaseInsensitive(t *testing.T) {
	got := providerCandidates("zai")
	if len(got) != 2 || got[0] != "zai" || got[1] != "zhipuai" {
		t.Fatalf("alias not resolved: %v", got)
	}
	// Mixed-case input must resolve too.
	got = providerCandidates("Zai")
	if len(got) != 2 || got[1] != "zhipuai" {
		t.Fatalf("alias not resolved for mixed case: %v", got)
	}
}
```

（若 catalog_test.go 是 `package catalog_test` 外部测试包，则放入 catalog 包内的测试文件；先检查该文件的 package 声明。)

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./pkg/llm/catalog -run TestProviderCandidatesAliasCaseInsensitive -v`
Expected: FAIL —— `"Zai"` 大小写不匹配时 alias 不生效

- [ ] **Step 3: 实现修复**

`catalog.go` 的 alias 表键改为小写，`providerCandidates` 查找前归一化：

```go
var providerAliases = map[string]string{
	"moonshot": "moonshotai",
	"gemini":   "google",
	"zai":      "zhipuai",
}

// providerCandidates returns the provider names to try for a lookup: the given
// name first, then its models.dev alias (if any). Ordering keeps an exact,
// same-name match ahead of the aliased one.
func providerCandidates(provider string) []string {
	if alias, ok := providerAliases[strings.ToLower(provider)]; ok {
		return []string{provider, alias}
	}
	return []string{provider}
}
```

（`strings` 已在 catalog.go import 中。)

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./pkg/llm/catalog -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/llm/catalog/catalog.go pkg/llm/catalog/catalog_test.go
git commit -m "fix(llm/catalog): make provider alias lookup case-insensitive"
```

---

### 收尾：全量验证

- [ ] **Step: 全仓回归 + race**

```bash
go build ./...
go vet $(go list ./... | grep -v 'reference/Shannon')
go test $(go list ./... | grep -v 'reference/Shannon') -count=1
go test ./pkg/llm/... -race -count=1
```

Expected: 全部 PASS。注意 `pkg/agent`、`pkg/tui` 依赖 llm 语义，重点确认没有测试仍假设"HTTP 错误经 channel 传递"。

---

## Self-Review 记录

- **Spec 覆盖：** 审查报告"一、必须修"6 条 → Task 1-6 一一对应。报告中的次要项（超时看门狗、failover、catalog 周期刷新等）属于第二/三批，不在本计划范围。
- **Placeholder 扫描：** 无 TBD；所有代码块为完整可编译代码。Task 4 Step 1 对 `captureRequestBody` 签名有一个前置确认点，已在步骤内注明。
- **类型一致性：** `doStreamRequest(client *http.Client, req *http.Request) (*http.Response, error)`;`forward(ctx, prov, model, src, out)`;`EventError{Code, Message}` —— 与现有定义一致。Task 2 先于 Task 5 改 engine.go,Task 5 的 `StreamTurn` 片段以 Task 2 合并后为准（两处改动不重叠：forward 调用行 vs providers 读取行）。
