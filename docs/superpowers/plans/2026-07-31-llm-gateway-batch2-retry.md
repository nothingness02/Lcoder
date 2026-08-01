# LLM Gateway 第二批:重试体系 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把重试体系补全到参考项目(opencode/kimi/pi)的共识水位:Retry-After 头优先、指数退避加 jitter 和上限、context overflow 单独归类永不重试、流内"未收到任何内容"的错误可整轮重试。

**Architecture:** 传输层(pkg/llm/retry.go + provider/adapter.go)负责 status 级重试与退避策略;会话层(pkg/agent/streamer.go)负责"建流成功但流出任何内容前就失败"的整轮重试。已从 StreamTurnRetry 返回的错误不再被 streamer 二次重试(避免 3×3 嵌套放大)。

**Tech Stack:** Go 1.25(math/rand/v2 做 jitter),httptest + llmtest.ScriptAdapter 测试。

**前置状态:** master 已含第一批 6 个修复。新分支 `fix/llm-gateway-batch2`。

---

### Task 1: Retry-After 头解析 + 退避 jitter/上限

**Files:**
- Modify: `pkg/llm/provider/event.go`(EventError 加字段)
- Modify: `pkg/llm/provider/adapter.go`(classifyHTTP 解析头,doStreamRequest 传头)
- Modify: `pkg/llm/retry.go`(RetryConfig 加 MaxBackoff,退避计算加 jitter/cap/Retry-After 优先)
- Test: `pkg/llm/provider/adapter_test.go`(头解析单测)
- Test: `pkg/llm/retry_test.go`(Retry-After 等待时长集成测试、退避上限单测)

- [ ] **Step 1: classifyHTTP 头解析失败测试**

`pkg/llm/provider/adapter_test.go` 追加:

```go
func TestClassifyHTTPRetryAfter(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		want    time.Duration
	}{
		{"seconds", map[string]string{"Retry-After": "2"}, 2 * time.Second},
		{"millis", map[string]string{"Retry-After-Ms": "150"}, 150 * time.Millisecond},
		{"http-date", map[string]string{"Retry-After": time.Now().Add(3 * time.Second).UTC().Format(http.TimeFormat)}, 0}, // 只校验 >0
		{"absent", nil, 0},
		{"garbage", map[string]string{"Retry-After": "soon"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tc.headers {
				h.Set(k, v)
			}
			err := classifyHTTP(429, []byte(`{}`), h)
			if tc.name == "http-date" {
				if err.RetryAfter <= 0 {
					t.Fatalf("http-date Retry-After not parsed: %+v", err)
				}
				return
			}
			if err.RetryAfter != tc.want {
				t.Fatalf("RetryAfter = %v, want %v", err.RetryAfter, tc.want)
			}
		})
	}
}
```

(adapter_test.go 需加 `"time"` import。)

Run: `go test ./pkg/llm/provider -run TestClassifyHTTPRetryAfter -v`
Expected: FAIL —— classifyHTTP 当前只收 (status, body) 两个参数,编译错误

- [ ] **Step 2: 退避策略失败测试**

`pkg/llm/retry_test.go` 追加:

```go
func TestBackoffHonorsRetryAfter(t *testing.T) {
	rc := RetryConfig{MaxAttempts: 3, BaseBackoff: time.Second, MaxBackoff: 32 * time.Second}
	if d := Backoff(rc, 0, 150*time.Millisecond); d != 150*time.Millisecond {
		t.Fatalf("Retry-After must win over computed backoff, got %v", d)
	}
}

func TestBackoffCapped(t *testing.T) {
	rc := RetryConfig{MaxAttempts: 10, BaseBackoff: time.Second, MaxBackoff: 5 * time.Second}
	for attempt := 0; attempt < 10; attempt++ {
		if d := Backoff(rc, attempt, 0); d > rc.MaxBackoff {
			t.Fatalf("attempt %d backoff %v exceeds cap %v", attempt, d, rc.MaxBackoff)
		}
	}
	// Retry-After 也封顶,但放宽到硬上限(供应商可能要求分钟级等待)。
	if d := Backoff(rc, 0, 10*time.Minute); d != maxRetryAfterHonor {
		t.Fatalf("Retry-After hard cap = %v, want %v", d, maxRetryAfterHonor)
	}
}

func TestStreamTurnRetryWaitsForRetryAfter(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After-Ms", "200")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)

	eng := engine.New(catalog.New(catalog.Options{Refresh: false}))
	eng.RegisterProvider("openai", provider.Conn{BaseURL: srv.URL, Route: "openai"})
	c := NewClient(eng)
	start := time.Now()
	stream, err := c.StreamTurnRetry(context.Background(), models.TurnRequest{
		Model: models.ModelRef{Provider: "openai", ID: "gpt-4o"},
	}, RetryConfig{MaxAttempts: 3, BaseBackoff: time.Millisecond, MaxBackoff: 32 * time.Second})
	if err != nil {
		t.Fatalf("retry should recover: %v", err)
	}
	for range stream {
	}
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Fatalf("Retry-After-Ms 200 not honored, elapsed %v", elapsed)
	}
}
```

Run: `go test ./pkg/llm -run 'TestBackoff|TestStreamTurnRetryWaitsForRetryAfter' -v`
Expected: FAIL —— Backoff / maxRetryAfterHonor 未定义,Retry-After 不被等待

- [ ] **Step 3: 实现 —— EventError 加 RetryAfter,classifyHTTP 解析头**

`pkg/llm/provider/event.go` 的 EventError 加字段:

```go
// EventError is a classified provider failure carried on KindError.
type EventError struct {
	Code          string         // bad_request | auth | rate_limit | internal | context_overflow
	Message       string         //
	ProviderError map[string]any //
	// RetryAfter is the provider-requested wait before retrying (from the
	// Retry-After / Retry-After-Ms response headers); 0 when absent.
	RetryAfter time.Duration
}
```

(import 加 `"time"`。)

`pkg/llm/provider/adapter.go`:classifyHTTP 改签名并解析头,doStreamRequest 传入:

```go
// classifyHTTP maps a provider HTTP status + body to a normalized EventError.
// 400 responses whose body names a context-length limit are classified as
// context_overflow (never retryable; the agent layer routes it to compaction).
func classifyHTTP(status int, body []byte, headers http.Header) *EventError {
	code := "internal"
	switch {
	case status == 429:
		code = "rate_limit"
	case status == 401 || status == 403:
		code = "auth"
	case status == 400:
		code = "bad_request"
		if isContextOverflowBody(body) {
			code = "context_overflow"
		}
	}
	pe := map[string]any{}
	_ = json.Unmarshal(body, &pe)
	return &EventError{Code: code, Message: string(body), ProviderError: pe, RetryAfter: parseRetryAfter(headers)}
}

// parseRetryAfter reads Retry-After (seconds or HTTP-date) and Retry-After-Ms.
func parseRetryAfter(h http.Header) time.Duration {
	if ms := h.Get("Retry-After-Ms"); ms != "" {
		if n, err := strconv.Atoi(ms); err == nil && n > 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	ra := h.Get("Retry-After")
	if ra == "" {
		return 0
	}
	if n, err := strconv.Atoi(ra); err == nil {
		if n < 0 {
			return 0
		}
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(ra); err == nil {
		return max(time.Until(t), 0)
	}
	return 0
}

// isContextOverflowBody reports whether a 400 body says the prompt exceeded
// the model's context window (openai "context_length_exceeded", anthropic
// "prompt is too long", generic "maximum context length").
func isContextOverflowBody(body []byte) bool {
	s := strings.ToLower(string(body))
	return strings.Contains(s, "context_length_exceeded") ||
		strings.Contains(s, "prompt is too long") ||
		strings.Contains(s, "maximum context length")
}
```

doStreamRequest 中 `classifyHTTP(resp.StatusCode, data)` 改为 `classifyHTTP(resp.StatusCode, data, resp.Header)`。
(adapter.go import 加 `"strconv"`、`"strings"`、`"time"`。)

- [ ] **Step 4: 实现 —— retry.go 退避策略**

`pkg/llm/retry.go`:

```go
// maxRetryAfterHonor caps how long a provider-requested Retry-After wait is
// honored; computed backoff is capped at RetryConfig.MaxBackoff instead.
const maxRetryAfterHonor = 2 * time.Minute

// RetryConfig controls turn-establishment retries.
type RetryConfig struct {
	MaxAttempts int
	BaseBackoff time.Duration
	// MaxBackoff caps the computed exponential backoff (jitter included).
	// Zero means 32s.
	MaxBackoff time.Duration
}

// DefaultRetryConfig retries up to 3 times with 1s/2s exponential backoff.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{MaxAttempts: 3, BaseBackoff: time.Second, MaxBackoff: 32 * time.Second}
}

// Backoff computes the wait before the next attempt. A provider-supplied
// RetryAfter always wins (capped at maxRetryAfterHonor); otherwise exponential
// backoff with ±25% jitter, capped at MaxBackoff.
func Backoff(rc RetryConfig, attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return min(retryAfter, maxRetryAfterHonor)
	}
	cap := rc.MaxBackoff
	if cap <= 0 {
		cap = 32 * time.Second
	}
	d := rc.BaseBackoff << attempt
	if d <= 0 || d > cap {
		d = cap
	}
	// ±25% jitter; never below half the uncapped value, never above the cap.
	jitter := time.Duration(float64(d) * (0.75 + 0.5*rand.Float64()))
	return min(jitter, cap)
}

// retryAfterOf extracts the provider-requested wait from an error, if any.
func retryAfterOf(err error) time.Duration {
	var pe *provider.EventError
	if errors.As(err, &pe) {
		return pe.RetryAfter
	}
	return 0
}
```

(import 加 `"math/rand/v2"`;`rand.Float64()` 用 v2。)StreamTurnRetry 中 `backoff := rc.BaseBackoff << attempt` 改为 `backoff := Backoff(rc, attempt, retryAfterOf(lastErr))`。

- [ ] **Step 5: 跑测试确认通过 + 全量回归**

Run: `go test ./pkg/llm/... -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/llm/provider/event.go pkg/llm/provider/adapter.go pkg/llm/provider/adapter_test.go pkg/llm/retry.go pkg/llm/retry_test.go
git commit -m "feat(llm): honor Retry-After and add jittered, capped backoff"
```

---

### Task 2: context_overflow 归类(随 Task 1 Step 3 已并入实现)

说明:isContextOverflowBody 已包含在 Task 1 Step 3 的 classifyHTTP 重写里(classifyHTTP 签名只有一处,拆开改会来回冲突)。本 task 只补测试与文档注释。

**Files:**
- Test: `pkg/llm/provider/openai_test.go`

- [ ] **Step 1: 写测试**

`pkg/llm/provider/openai_test.go` 追加:

```go
func TestOpenAIStreamContextOverflowClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"This model's maximum context length is 128000 tokens.","code":"context_length_exceeded"}}`))
	}))
	t.Cleanup(srv.Close)
	ad := OpenAICompat{}
	_, err := ad.Stream(context.Background(), Conn{BaseURL: srv.URL, Route: "openai"},
		models.TurnRequest{Model: models.ModelRef{Provider: "openai", ID: "gpt-4o"}})
	var pe *EventError
	if !errors.As(err, &pe) || pe.Code != "context_overflow" {
		t.Fatalf("want context_overflow EventError, got %v", err)
	}
}
```

- [ ] **Step 2: 确认 IsRetryable 对 context_overflow 为 false 的测试**

`pkg/llm/retry_test.go` 的 TestIsRetryable cases 切片加一行:

```go
		{"context-overflow", &provider.EventError{Code: "context_overflow"}, false},
```

- [ ] **Step 3: 跑测试 + commit**

Run: `go test ./pkg/llm/... -count=1`
Expected: PASS

```bash
git add pkg/llm/provider/openai_test.go pkg/llm/retry_test.go
git commit -m "feat(llm): classify context-overflow 400s as a distinct non-retryable code"
```

---

### Task 3: streamer 整轮重试(流内 pre-content 错误)

**语义:** StreamTurnRetry 返回的 error 直接上抛(它已耗尽建流重试);只有 consume 阶段在**未收到任何内容事件**(KindStart/TextDelta/ThinkingDelta/ToolCallDelta 均未出现)时收到可重试的 KindError,才整轮重试。重试重新走 StreamTurnRetry + consume。总轮次 = RetryConfig.MaxAttempts。

**Files:**
- Modify: `pkg/agent/streamer.go:69-186`
- Test: `pkg/agent/`(用 llmtest.NewScript:第一个脚本 pre-content KindError rate_limit,第二个脚本正常)

- [ ] **Step 1: 写失败测试**

先看 `pkg/agent/builder_test.go` 里现有构造 agent + llmtest client 的最小 harness,复用之。测试核心:

```go
// 第一个 turn:建流成功后立刻 rate_limit KindError(pre-content);
// 第二个 turn:正常流出。修复前:第一轮即失败;修复后:整轮重试成功,
// 且 adapter 被调用了 2 次。
```

(实现时按 builder_test.go 的实际 helper 调整;断言:Run/Prompt 返回成功、adapter.CallCount()==2、消息内容来自第二个脚本。)

再补一个反例测试:第一个脚本先给 KindTextDelta 再给 KindError rate_limit —— **不得重试**(partial 已流出,错误直接上抛,CallCount()==1)。

Run: 两个测试均 FAIL(当前无任何整轮重试)

- [ ] **Step 2: 实现**

`streamer.stream` 重构:

```go
	rc := llm.DefaultRetryConfig()
	var msg models.AgentMessage
	var lastErr error
	for attempt := 0; attempt < rc.MaxAttempts; attempt++ {
		stream, err := s.llm.StreamTurnRetry(streamCtx, req, rc)
		if err != nil {
			// 建流阶段已重试耗尽,不再嵌套重试。
			return models.AgentMessage{}, err
		}
		gotContent, m, err := s.consume(streamCtx, stream, turn)
		if err == nil {
			msg = m
			lastErr = nil
			break
		}
		lastErr = err
		if gotContent || !llm.IsRetryable(err) || attempt == rc.MaxAttempts-1 {
			return models.AgentMessage{}, err
		}
		// 未流出任何内容的流内失败:整轮重试是安全的。
		backoff := llm.Backoff(rc, attempt, 0)
		timer := time.NewTimer(backoff)
		select {
		case <-streamCtx.Done():
			timer.Stop()
			return models.AgentMessage{}, lastErr
		case <-timer.C:
		}
	}
	if lastErr != nil {
		return models.AgentMessage{}, lastErr
	}
	return msg, nil
```

consume = 现有 69-186 行的事件循环原样抽出,签名 `func (s *streamer) consume(streamCtx context.Context, stream <-chan provider.Event, turn int) (gotContent bool, msg models.AgentMessage, err error)`。gotContent = started(即是否 emit 过 MessageStart/收到过内容)。注意保留现有的 usage 记录、TTFT、partial 语义;consume 内部 turnStartTime 逻辑随搬。

- [ ] **Step 3: 跑测试确认通过 + 全量回归**

Run: `go test ./pkg/agent/... -count=1` 然后 `go test $(go list ./... | grep -v 'reference/Shannon') -count=1`
Expected: PASS(skills 两个既有失败除外)

- [ ] **Step 4: Commit**

```bash
git add pkg/agent/streamer.go pkg/agent/<test file>
git commit -m "feat(agent): retry whole turn when stream fails before any content"
```

---

### 收尾:全量验证

- [ ] **Step: build + vet + test + race**

```bash
go build ./...
go vet $(go list ./... | grep -v 'reference/Shannon')
go test $(go list ./... | grep -v 'reference/Shannon') -count=1
CGO_ENABLED=1 go test ./pkg/llm/... ./pkg/agent/... -race -count=1
```

Expected: PASS(两个 skills 既有失败除外——已确认与 llm/agent 无关)

---

## Self-Review 记录

- **Spec 覆盖:** 报告第二批四项:retry-after(Task 1)、jitter/cap(Task 1)、流内未收内容整轮重试(Task 3)、context overflow 归类(Task 1+2)。覆盖齐。
- **Placeholder 扫描:** Task 3 Step 1 的测试代码引用 builder_test.go 的既有 harness,需实现时对齐;其余代码完整。
- **类型一致性:** `classifyHTTP(status int, body []byte, headers http.Header)`、`EventError.RetryAfter time.Duration`、`Backoff(rc RetryConfig, attempt int, retryAfter time.Duration) time.Duration`、`consume(...) (bool, models.AgentMessage, error)` —— 全文一致。classifyHTTP 仅 doStreamRequest 一处调用(第一批已收敛),签名改动无遗漏调用点。
- **嵌套重试防护:** StreamTurnRetry 返回的错误直接上抛,streamer 只重试 consume 阶段错误——两轮不会相乘。
