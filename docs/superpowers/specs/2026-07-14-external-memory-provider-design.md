# 外部记忆 Provider 接口（Hermes 风格）设计

## 目标
在 Lcoder 中实现一组类似 Hermes 的 `memory.Provider` 生命周期接口，首版以通用 HTTP/REST provider 为例，支持：
- `Prefetch(ctx, query string) ([]string, error)`：每轮用户输入后召回相关记忆。
- `SyncTurn(ctx, user, assistant string) error`：每轮 assistant 回复后将本轮对话写入外部记忆。
- `OnSessionEnd(ctx, summary *SessionSummary) error`：会话结束时触发总结/会话写入。

## 架构

### 1. 接口层：`pkg/memory/provider.go`

```go
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

// NopProvider is the default no-op provider.
var NopProvider Provider = nopProvider{}
```

`Healthy` 用于熔断后的快速跳过。

### 2. HTTP Provider：`pkg/memory/httpprovider.go`

```go
type HTTPProviderConfig struct {
    Endpoint      string            `yaml:"endpoint"`
    APIKey        string            `yaml:"api_key"`
    Headers       map[string]string `yaml:"headers"`
    Timeout       int               `yaml:"timeout"` // seconds; 0 -> 10s
    SearchPath    string            `yaml:"search_path"`    // default /search
    ObservePath   string            `yaml:"observe_path"`   // default /observe
    SessionEndPath string           `yaml:"session_end_path"` // default /session/end
}
```

HTTP 映射（REST/JSON）：
- `Prefetch` → `POST {endpoint}{search_path}`，body `{"query":"..."}`，返回 `{"memories":["..."]}`。
- `SyncTurn` → `POST {endpoint}{observe_path}`，body `{"user":"...","assistant":"..."}`。
- `OnSessionEnd` → `POST {endpoint}{session_end_path}`，body `{"session_id":"...","turn_count":N}`。

Provider 内部维护：
- `http.Client`（可配置 timeout）。
- 简单熔断器：连续失败次数超过阈值后进入 OPEN 状态，一段时间后自动半开；OPEN 时 `Healthy` 返回 false。
- 请求/响应日志通过返回 error 由上层 `ErrorEvent` 处理。

### 3. Injector 聚合本地 + 外部记忆：`pkg/memory/injector.go`

Injector 扩展为持有 `providers []Provider`（目前只支持一个，但接口为数组预留）。

```go
type Injector struct {
    store      *Store
    providers  []Provider
    manager    *contextmgr.Manager
    ranker     Ranker
    maxTokens  int
    failureMsg string // last provider error text
}
```

`Prefetch` 流程：
1. 从本地 `Store` 召回条目（已有行为）。
2. 对每个 `Provider`：
   - 若 `Healthy()` 为 false，跳过并记录提示。
   - 调用 `Prefetch(ctx, query)`，结果追加。
   - 失败时记录 `failureMsg` 并触发 `ErrorEvent`。
3. 用 `Ranker` 对合并后条目排序。
4. 按 `maxTokens` 严格截断，写入 `memory_recall` block。
5. 若存在 provider 失败，在 block 末尾追加简短提示：`// External memory provider unavailable: <msg>`，满足“返回失败信息给 agent”的需求。

### 4. Agent Loop 集成：`pkg/agent/loop.go`

在现有 `MemoryInjector` 调用位置基础上扩展：

- `Prefetch`：已在 `refreshEphemeralReminders` 之后、`maybeCompact` 之前调用，保持不变。
- `SyncTurn`：在 `TurnEndEvent` 发出后、下轮开始前调用。需要捕获最近一轮的 user text 与 assistant text。
  - 在 `run` 中维护 `lastUserText` 和 `lastAssistantText`。
  - 在 turn 循环末尾调用 `a.memoryInjector.SyncTurn(ctx, lastUserText, lastAssistantText)`（通过类型断言或扩展接口）。
- `OnSessionEnd`：在 `run` 退出前、`AgentEndEvent` 之前调用。

为避免改变现有 `MemoryInjector` 接口的语义，引入新接口：

```go
// MemorySink receives turn/session data for external persistence.
type MemorySink interface {
    MemoryInjector
    SyncTurn(ctx context.Context, user, assistant string) error
    OnSessionEnd(ctx context.Context, summary TurnSummary) error
}
```

Agent 的 `MemoryInjector` 字段保持向后兼容；当注入的实例同时实现 `MemorySink` 时，调用 `SyncTurn` / `OnSessionEnd`。

### 5. 配置扩展：`pkg/config/config.go`

```go
type MemoryConfig struct {
    Enabled         bool                  `yaml:"enabled"`
    MemoryCharLimit int                   `yaml:"memory_char_limit"`
    UserCharLimit   int                   `yaml:"user_char_limit"`
    DynamicRecall   bool                  `yaml:"dynamic_recall"`
    RecallMaxTokens int                   `yaml:"recall_max_tokens"`
    RecallMinScore  float64               `yaml:"recall_min_score"`
    Providers       []MemoryProviderConfig `yaml:"providers"`
}

type MemoryProviderConfig struct {
    Name   string             `yaml:"name"`
    Type   string             `yaml:"type"` // "http"
    Config HTTPProviderConfig `yaml:"config"`
}
```

`configs/lcoder.yaml` 增加示例：

```yaml
memory:
  enabled: true
  dynamic_recall: true
  recall_max_tokens: 1024
  providers:
    - name: hermes-http
      type: http
      config:
        endpoint: "http://localhost:8000"
        api_key: "${HERMES_MEMORY_API_KEY}"  # 支持 {env:VAR} 展开由现有 resolveProviders 处理
        timeout: 10
```

### 6. 装配：`cmd/lcoder/main.go`

在 `prepareAgent` 中：
1. 读取 `cfg.Memory.Providers`。
2. 对每个 provider config，调用 `memory.NewHTTPProvider(cfg)` 创建实例。
3. 用 `memory.NewInjector(store, mgr, cfg.Memory.RecallMaxTokens).WithProviders(providers...)` 注入 agent builder。

### 7. 测试

- `pkg/memory/provider_test.go`：验证 `NopProvider` 行为。
- `pkg/memory/httpprovider_test.go`：使用 `httptest.Server` 测试 `Prefetch` / `SyncTurn` / `OnSessionEnd`、超时、重试、熔断。
- `pkg/memory/injector_test.go`：验证本地+外部结果聚合、provider 失败提示、预算截断。
- `pkg/agent/loop_test.go`（或新增 `memory_hook_test.go`）：使用 fake `MemorySink` 验证 `Prefetch` / `SyncTurn` / `OnSessionEnd` 调用时机。

## 关键决策

- 一个会话只支持一个活跃 external provider（`providers []Provider` 为未来预留，但配置只写一个）。
- 外部记忆召回失败不影响本轮回话；错误信息通过 `memory_recall` block 提示给模型。
- `SyncTurn` 不阻塞进入下一轮；错误仅记录 `ErrorEvent`。
- 严格按 `RecallMaxTokens` 截断合并后的结果，不保证至少一条。

请确认这套设计是否符合你的预期，或需要调整的地方。