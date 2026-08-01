# LLM Gateway 第三批:协议显式化 + 弹性能力 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 五项:(1) 协议族显式配置 fail-fast;(2) 同 provider 多 API key failover(失败摘除 + 冷却恢复);(3) per-provider 并发闸口;(4) catalog 周期刷新 + 状态暴露;(5) 观测出口(真实 Status + 重试事件)。

**Architecture:** `provider.Protocol` 一等类型,engine 按 Protocol 精确分派 adapter(无沉默兜底);engine 内维护 per-provider credential pool(选择/摘除/冷却)与并发信号量;catalog 增加 ticker 刷新循环与状态字段;`Client.Health` 无调用方,直接替换为结构化 `Status()`;重试经 `Client.OnRetry` 回调接到事件总线。

**前置:** master 含第一、二批。新分支 `fix/llm-gateway-batch3`。

**设计决策(已定,不再讨论):**
- route 保留三职中的两职(默认 BaseURL 查表、catalog 元数据查询),选 adapter 改由 Protocol 决定。
- Protocol 解析:config 显式 `protocol:` 优先(未知值 → 启动报错);缺省由 route 派生(`anthropic`→anthropic,`openai-responses`→openai-responses,其余 provider 名 route→openai-chat)。engine 层对空 Protocol 宽容派生(兼容测试/llmtest 不填),config 层严格。
- failover 只统计**建流阶段**失败(batch 1 后 HTTP 错误都在此);阈值 3 次连续失败摘除,冷却 60s 自动恢复,无主动探测。选择策略 round-robin;全部不可用时兜底轮询全部(不放空流量)。
- 并发闸口:`max_concurrent` > 0 时生效,信号量在 StreamTurn 获取、forward 结束时释放。
- catalog 周期刷新默认 1h,`Close()` 停止;失败写入状态字段,不打印(库不直接输出)。

---

### Task 1: 显式 Protocol + fail-fast

**Files:**
- Modify: `pkg/llm/provider/adapter.go`(Protocol 类型 + 解析 + 派生)
- Modify: `pkg/llm/engine/engine.go`(factory 按 Protocol 分派,StreamTurn 用 Protocol)
- Modify: `pkg/llm/catalog/resolve.go`(ResolvedProvider 带 Protocol,显式 protocol 校验)
- Modify: `pkg/config/providers.go`(ProviderConn 加 protocol/api_keys/max_concurrent)
- Modify: `pkg/llm/client.go`(RegisterProvider 映射新字段)
- Modify: `cmd/lcoder/wiring.go`(buildEngine 传 protocol)
- Test: `pkg/llm/provider/adapter_test.go`、`pkg/llm/engine/engine_test.go`、`pkg/llm/catalog/resolve_test.go`

- [ ] **Step 1: 失败测试**

adapter_test.go 追加:

```go
func TestProtocolParseAndDerive(t *testing.T) {
	// 显式解析:三个合法值 + 非法值报错
	for _, s := range []string{"openai-chat", "openai-responses", "anthropic"} {
		if _, err := ParseProtocol(s); err != nil {
			t.Fatalf("ParseProtocol(%q): %v", s, err)
		}
	}
	if _, err := ParseProtocol("gpt"); err == nil {
		t.Fatal("unknown protocol must error")
	}
	// route 派生
	if p := ProtocolForRoute("anthropic"); p != ProtocolAnthropic {
		t.Fatalf("route anthropic → %q", p)
	}
	if p := ProtocolForRoute("openai-responses"); p != ProtocolOpenAIResponses {
		t.Fatalf("route openai-responses → %q", p)
	}
	for _, r := range []string{"deepseek", "openai", "gemini", "xai", ""} {
		if p := ProtocolForRoute(r); p != ProtocolOpenAIChat {
			t.Fatalf("route %q → %q, want openai-chat", r, p)
		}
	}
}
```

engine_test.go 追加(factory 签名改为 Protocol + 未知 route 的 Conn 仍按 openai-chat 分派):

```go
func TestAdapterFactorySelectsByProtocol(t *testing.T) {
	cat := catalog.New(catalog.Options{Refresh: false})
	eng := New(cat)
	var got Protocol
	// ... 用 SetAdapterFactory 捕获 protocol 参数,断言:
	// Conn{Route:"deepseek"} → "openai-chat";Conn{Route:"anthropic"} → "anthropic";
	// Conn{Protocol:"openai-responses", Route:"openai"} → "openai-responses"(显式覆盖 route)
}
```

resolve_test.go 追加:显式 protocol 合法穿透、非法报错。

Run: `go test ./pkg/llm/... -run 'Protocol' -v` → 编译失败/断言失败

- [ ] **Step 2: 实现**

adapter.go 顶部(Conn 定义附近):

```go
// Protocol identifies the wire protocol an adapter speaks. It is a first-class
// concept separate from Route (which names the provider for base-URL defaults
// and catalog lookups), so a custom endpoint can declare its wire explicitly.
type Protocol string

const (
	ProtocolOpenAIChat      Protocol = "openai-chat"
	ProtocolOpenAIResponses Protocol = "openai-responses"
	ProtocolAnthropic       Protocol = "anthropic"
)

// ParseProtocol validates an explicitly configured protocol value.
func ParseProtocol(s string) (Protocol, error) {
	switch Protocol(s) {
	case ProtocolOpenAIChat, ProtocolOpenAIResponses, ProtocolAnthropic:
		return Protocol(s), nil
	}
	return "", fmt.Errorf("unknown protocol %q (want openai-chat | openai-responses | anthropic)", s)
}

// ProtocolForRoute derives the wire protocol from a route. Provider-name
// routes (deepseek, gemini, xai, ...) all speak OpenAI chat completions.
func ProtocolForRoute(route string) Protocol {
	switch route {
	case "anthropic":
		return ProtocolAnthropic
	case "openai-responses":
		return ProtocolOpenAIResponses
	default:
		return ProtocolOpenAIChat
	}
}
```

(adapter.go import 加 `"fmt"`。)Conn 加字段:

```go
type Conn struct {
	BaseURL  string
	APIKey   string
	APIKeys  []string          // failover 池;有值时优先于 APIKey(Task 2 用)
	Route    string
	Protocol Protocol          // 空 = 由 Route 派生
	Headers  map[string]string
	// MaxConcurrent 限制该 provider 的并发流数(0 = 不限,Task 3 用)
	MaxConcurrent int
}
```

engine.go:

```go
type AdapterFactory func(p provider.Protocol, marks provider.CacheMarks) provider.Adapter

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
```

StreamTurn 中:

```go
	proto := conn.Protocol
	if proto == "" {
		proto = provider.ProtocolForRoute(conn.Route)
	}
	anthropic := proto == provider.ProtocolAnthropic
	marks := provider.ComputeCacheMarks(req.Cache, req.CacheBreakpoints, len(req.Messages), anthropic)
	conn.BaseURL = provider.ResolveBaseURL(conn)
	...
	adapter := e.newAdapter(proto, marks)
```

(`ThinkingSpec(conn.Route, ...)` 那行保持用 route 不变。)同步更新 SetAdapterFactory 注释、engine_test.go 与 llmtest.go 的 factory lambda 签名(`func(p provider.Protocol, marks provider.CacheMarks)`)。

resolve.go:ResolvedProvider 加 `Protocol provider.Protocol`;ResolveProvider 签名加 conProtocol 参数:非空时 `provider.ParseProtocol` 校验(错则返回 error),空则由最终 route 派生。

config/providers.go ProviderConn 加:

```go
	Protocol      string   `yaml:"protocol"       json:"protocol,omitempty"`
	APIKeys       []string `yaml:"api_keys"       json:"api_keys,omitempty"`
	MaxConcurrent int      `yaml:"max_concurrent" json:"max_concurrent,omitempty"`
```

client.go RegisterProvider 映射:`Protocol: parse 或 ""/派生`(容错:TUI 热注册不填,留空由 engine 派生;填错则返回 error 给 TUI 显示)、`APIKeys: conn.APIKeys`、`MaxConcurrent: conn.MaxConcurrent`。wiring.go buildEngine 把 `conn.Protocol` 传给 ResolveProvider 并用返回的 Protocol 填 Conn。

- [ ] **Step 3: 回归 + commit**

`go build ./... && go test ./pkg/llm/... ./pkg/config/... ./cmd/... -count=1` → PASS

```bash
git commit -m "refactor(llm): make wire protocol an explicit, validated field"
```

---

### Task 2: 多 key failover 状态机

**Files:**
- Modify: `pkg/llm/engine/engine.go`(credential pool)
- Modify: `cmd/lcoder/wiring.go`(Conn 已带 APIKeys,Task 1 已映射)
- Test: `pkg/llm/engine/engine_test.go`

**机制:** engine 内 `pools map[string]*credPool`(与 providers 同一把锁)。credPool:

```go
type credential struct {
	key             string
	failures        int
	unavailableUntil time.Time
}
type credPool struct {
	creds []credential
	next  int // round-robin 游标
}
```

- 建池时机:RegisterProvider 时,若 `len(conn.APIKeys) > 0` 建池(APIKey 非空也并入队首);否则删池(单 key 无 failover)。
- `selectCredential(prov) string`:遍历池,跳过 `failures >= 3 && now < unavailableUntil` 的;从 next 起 round-robin;全部不可用则无视状态直接轮询(不放空)。返回 ""表示无池(用 conn.APIKey 原样)。
- StreamTurn:`if key := e.selectCredential(prov); key != "" { conn.APIKey = key }`;adapter.Stream 返回 establishment error → `reportFailure(prov, conn.APIKey)`(failures++,到 3 则 unavailableUntil = now+60s);成功 → `reportSuccess`(failures=0, unavailableUntil 清零)。
- 参数做成 Engine 字段(`failThreshold=3`、`cooldown=60s`,New() 填默认)以便测试注入小值。

- [ ] **Step 1: 失败测试**

```go
// 假 adapter:按 conn.APIKey 决定成败(k1 建流即 429,k2 正常)。
// 断言:1) 第一次 StreamTurn 用 k1 失败被记录;2) 足够多次 StreamTurn 后 k1 被摘除
// (连续 3 次失败),之后只见到 k2;3) cooldown 缩短注入后,k1 恢复轮换。
```

- [ ] **Step 2: 实现 + Step 3: 回归 + commit**

```bash
git commit -m "feat(llm/engine): multi-key failover with failure-bench and cooldown"
```

---

### Task 3: per-provider 并发闸口

**Files:**
- Modify: `pkg/llm/engine/engine.go`

**机制:** RegisterProvider 时若 `conn.MaxConcurrent > 0` 建 `sems map[string]chan struct{}`(cap = MaxConcurrent)。StreamTurn 在 adapter 选择前:

```go
	if sem := e.sems[prov]; sem != nil {
		select {
		case sem <- struct{}{}:
			released := false
			defer func() { // StreamTurn 出错路径释放
				if !released { <-sem }
			}()
			... adapter.Stream 成功后把释放责任转交 forward:
			go e.forward(ctx, prov, req.Model.ID, src, out, func() { <-sem })
			released = true
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
```

(forward 加 `onDone func()` 参数,defer 中调用。)

- [ ] **Step 1: 失败测试** — MaxConcurrent=1:第一个 turn 的流不读完,第二个 StreamTurn 阻塞;读完/取消后第二个放行。
- [ ] **Step 2: 实现 + Step 3: 回归 + commit**

```bash
git commit -m "feat(llm/engine): per-provider concurrency gate"
```

---

### Task 4: catalog 周期刷新 + 状态暴露

**Files:**
- Modify: `pkg/llm/catalog/catalog.go`
- Modify: `cmd/lcoder/wiring.go`(defer Close)
- Test: `pkg/llm/catalog/refresh_test.go`

**机制:**

```go
type Options struct {
	Refresh         bool
	RefreshInterval time.Duration // 0 = 1h 默认;<0 同 false
	CachePath, SourceURL string
	Overrides []Entry
}

// Catalog 增加:
//   stopCh chan struct{}, done chan struct{}
//   lastRefreshAt time.Time, lastRefreshErr error(锁保护)
// Status() (time.Time, error);Close()
```

New() 的 `go c.refresh(...)` 改为 `go c.refreshLoop(cachePath, interval)`:先跑一轮现有 refresh 逻辑,然后 ticker 循环(每轮强制走网络,跳过 5min cache 短路——cache 只用于启动加速)。每轮记录 lastRefreshAt/lastRefreshErr。Close 关 stopCh 停循环。

- [ ] **Step 1: 失败测试** — httptest 假 models.dev,RefreshInterval=20ms,先返回 v1 再返回 v2,断言 catalog 最终含 v2 模型;服务端 500 时 Status() 暴露 error。
- [ ] **Step 2: 实现 + Step 3: 回归 + commit**

```bash
git commit -m "feat(llm/catalog): periodic refresh with observable status"
```

---

### Task 5: 观测出口(Status + 重试事件)

**Files:**
- Modify: `pkg/llm/client.go`(Health → Status)
- Modify: `pkg/llm/retry.go`(OnRetry 回调)
- Modify: `pkg/events/`(LLMRetryEvent)
- Modify: `pkg/agent/streamer.go`(第 2 层重试发事件)
- Modify: `cmd/lcoder/wiring.go`(接总线)
- Test: 各层

**机制:**

1. `Client.Status()` 替换 `Health()`(无调用方,直接删):

```go
type ProviderStatus struct {
	Route         string
	Protocol      string
	Credentials   int  // 池大小(0 = 单 key)
	Available     int  // 未被摘除的 credential 数
	MaxConcurrent int
	InFlight      int  // 闸口内占用
}
type Status struct {
	Providers            map[string]ProviderStatus
	CatalogLastRefreshAt time.Time
	CatalogLastError     string
}
func (c *Client) Status(ctx context.Context) Status
```

engine 侧加对应查询(读锁收集);catalog 侧接 Task 4 的 Status。

2. 重试事件:`pkg/events` 加

```go
const LLMRetry EventType = "llm.retry"
type LLMRetryEvent struct {
	Base
	Layer   string // "establish" | "turn"
	Attempt int
	Wait    time.Duration
	Err     string
}
```

`Client` 加 `OnRetry func(layer string, attempt int, wait time.Duration, err error)`;StreamTurnRetry 每次等待前回调("establish")。wiring.go 里 `llmClient.OnRetry = func(...) { bus.Emit(ctx, events.LLMRetryEvent{...}) }`。streamer 第 2 层重试用自己的 emitter 发("turn" 层)。

- [ ] **Step 1: 失败测试** — Status 反映池/闸口/catalog 状态;OnRetry 在 429 重试时被回调且 wait 符合 Retry-After。
- [ ] **Step 2: 实现 + Step 3: 回归 + commit**

```bash
git commit -m "feat(llm): structured gateway status and retry events"
```

---

### 收尾:全量验证

```bash
go build ./...
go vet $(go list ./... | grep -v 'reference/Shannon')
go test $(go list ./... | grep -v 'reference/Shannon') -count=1
CGO_ENABLED=1 go test ./pkg/llm/... ./pkg/agent/... -race -count=1
```

预期:PASS(skills 若仍失败需重查——6197b44 可能已修)。

**配置对齐检查(记忆:config 改动须对齐三场景):** ProviderConn 新增 protocol/api_keys/max_concurrent 后,grep configs/、test/、eval 相关 yaml 与代码构造点,确认零值语义不变(不填 = 旧行为)。

---

## Self-Review 记录

- **Spec 覆盖:** 第三批报告四项 + 显式 protocol,五项各一个 task。
- **Placeholder 扫描:** Task 2/3/4/5 的测试代码给了核心断言描述而非完整代码——执行时按 engine_test/catalog 现有 harness 展开(harness 已在前两批验证过)。
- **类型一致性:** `Protocol`、`AdapterFactory(Protocol, CacheMarks)`、`credPool`、`sems`、`Status`/`ProviderStatus`、`LLMRetryEvent` 全文一致;Task 3 改 forward 签名(加 onDone),Task 2 已先改 StreamTurn 内部,两 task 都动 engine.go StreamTurn,顺序执行 Task2→Task3 时以 Task 2 结果为基。
- **依赖顺序:** Task 1(Protocol/Conn 字段)→ Task 2(用 APIKeys)→ Task 3(用 MaxConcurrent,改 forward)→ Task 4(catalog Status)→ Task 5(聚合 Status + 事件)。
