# 设计:网关侧 model 路由 + TUI 供应商/模型/密钥配置

日期:2026-06-28
状态:待实现(spec)

## 1. 背景与问题

当前供应商/模型/密钥的配置心智负担集中在用户手写文件上:

- `provider` 与 `model` 在 `config.yaml` 顶层手填。
- api_key 只能走两条路:litellm 的标准环境变量(如 `OPENAI_API_KEY`),或
  `config.providers.<name>.api_key` 的 `{env:VAR}` 引用。
- 没有 TUI 入口,新用户首次使用必须先读文档、手改 YAML、设环境变量。
- 路由知识(model 属于哪个 provider、走哪个接口)分散:Go 侧 `TurnRequest.Model`
  必填 `provider`,网关 `stream_turn` 再按该 provider 取连接覆盖。

目标:**简化配置负担,提供 TUI 选择供应商/模型并配置密钥,同时保留纯文件配置等价路径,
并支持运行中动态切换供应商与模型而不打断会话。**

## 2. 核心思路:路由层下沉到网关

把 "model → provider → 接口" 的路由权威从 Go 下沉到网关(Python)。Go 侧只负责
**收集凭据 + 渲染选择 + 传回选中的 model**;网关按传入 model 自行解析 provider 并路由。

这一方向复用了现有代码已铺好的基础:

- 网关已有 `GET /v1/models`,返回带 `provider` 的 `ModelInfo`(`pkg/models/message.go:317`)。
- 网关 `registry.py` 已有 litellm 自动发现(`_discover_from_litellm`)。
- `models.yaml` 本身就是 `model→provider` 的共享真相源,已经过 `LCODER_MODELS_CONFIG`
  传给网关子进程。
- `stream_turn` 已按 `request.model.provider` 取连接覆盖
  (`gateway/lcoder_gateway/server.py:141,148`)。

## 3. 职责划分

| 层 | 职责 |
|---|---|
| 网关(Python) | model 权威:合并三源为统一 registry;按传入 model 解析 provider 并路由 litellm;运行中经 reload 端点热更新连接 |
| Go | 收集凭据(TUI/文件)→ 传网关;消费 `/v1/models` 渲染候选;传回选中的 model;首启检测与切换编排 |

## 4. 网关侧 model registry(三源合并)

```
(a) models.yaml      显式 model→provider + 窗口/能力/定价   来源:LCODER_MODELS_CONFIG(已传)
(b) litellm 发现      自动 model→provider + 窗口            来源:registry.py _discover_from_litellm(已有)
(c) _provider_conns  每 provider 的 base_url/key/route 连接  来源:LCODER_PROVIDERS(已传)+ reload 端点
```

新增能力:`registry.resolve(model_id) -> (provider, conn)`

- 优先用 (a) 中该 model 的显式 `provider`;
- 否则用 litellm 的 `get_llm_provider` 推断;
- 用解析出的 provider 去 (c) 取连接覆盖(可能为空,表示用 litellm 默认路由)。

`GET /v1/models` 输出维持不变(已含 provider/context_window),作为 TUI 的模型候选来源
(实现 "网络拉取模型列表")。

## 5. TurnRequest 简化:provider 可选

`ModelRef`(`pkg/models/message.go:307`)的 `Provider` 由必填降为**可选覆盖**:

```go
type ModelRef struct {
    Provider string `json:"provider,omitempty"` // 可选;留空则由网关 registry 解析
    ID       string `json:"id"`
}
```

网关 `stream_turn` 在路由前补一步缺省解析:

```python
provider = request.model.provider or model_registry.resolve(request.model.id).provider
# 之后照旧:litellm_model(provider, id, conns) / completion_overrides(provider, conns)
```

- Go 端默认可留空,依赖网关解析;TUI 选 model 时从 `/v1/models` 的 `ModelInfo.Provider`
  得知其 provider,可一并填入做显式 override(用于跨 provider 同名消歧)。
- 兼容性:旧调用仍填 provider,行为不变。

## 6. 凭据收集与传递(Go 侧)

### 6.1 配置分层

| 文件/来源 | 内容 | 维护方 |
|---|---|---|
| `config.yaml` | `provider` + `model` + 行为配置,**不含 key** | 用户手写 / TUI 写回 |
| `credentials.yaml`(新,`0600`,默认 gitignore) | 各 provider 的 `api_key` [+ 可选 `base_url`/`headers`] | TUI 写入,也可手改 |
| 内置 provider 表(新,Go 常量) | 显示名、默认 `base_url`、标准 key 的 env 名、litellm `route` | 项目内置 |
| `models.yaml` | 模型元数据(窗口/能力/定价) | 现状不变 |

`credentials.yaml` 示例:

```yaml
openai:
  api_key: sk-...
moonshot:
  api_key: sk-...
  base_url: https://api.moonshot.cn/v1   # 可选;内置表已带默认
```

### 6.2 api_key 解析优先级(每 provider)

```
config.providers.<name>.api_key (手写,含 {env:VAR})
  > credentials.yaml.<name>.api_key (TUI 写入)
  > 标准环境变量 (内置表的 KeyEnv,如 OPENAI_API_KEY)
```

三者归一到现有 `cfg.Providers` map → 复用 `resolveProviders` 展开 `{env:VAR}`
(`pkg/config/providers.go:33`)→ `LCODER_PROVIDERS` JSON → 网关。

### 6.3 内置 provider 表

```go
// pkg/config/builtin_providers.go
type ProviderInfo struct {
    Name        string // 内部标识,如 "openai"
    Display     string // TUI 显示名
    KeyEnv      string // 标准 api_key 环境变量名
    Route       string // litellm 协议前缀(默认同 Name)
    DefaultBase string // 非标准 base_url(可空)
}

var BuiltinProviders = []ProviderInfo{
    {Name: "openai",     Display: "OpenAI",          KeyEnv: "OPENAI_API_KEY",     Route: "openai"},
    {Name: "anthropic",  Display: "Anthropic",       KeyEnv: "ANTHROPIC_API_KEY",  Route: "anthropic"},
    {Name: "deepseek",   Display: "DeepSeek",        KeyEnv: "DEEPSEEK_API_KEY",   Route: "deepseek"},
    {Name: "moonshot",   Display: "Moonshot (Kimi)", KeyEnv: "MOONSHOT_API_KEY",   Route: "openai",
        DefaultBase: "https://api.moonshot.cn/v1"},
    {Name: "gemini",     Display: "Google Gemini",   KeyEnv: "GEMINI_API_KEY",     Route: "gemini"},
    {Name: "openrouter", Display: "OpenRouter",      KeyEnv: "OPENROUTER_API_KEY", Route: "openrouter"},
}
```

服务两件事:TUI 渲染 provider 列表;key 解析时知道每 provider 的标准 env 名。
provider 的连接默认大多 litellm 已内置,此表只补 UI 需求与少数非标准 base_url。

## 7. 运行中动态切换(方案 A:reload 端点)

网关启动时把**所有已配置 provider**(credentials + config.providers)灌入 `_provider_conns`。

- **切到已配 provider 的任意 model**:Go 改 `cfg.Model`(可能跨 provider),重发 `TurnRequest`,
  网关查 registry 路由——**零重启、不打断会话**。切换后 Go 重新解析上下文预算
  (按新 model 的 litellm 窗口走现有 `ResolveContextBudget`)。
- **运行中新增一个全新 provider 的 key**:网关 `_provider_conns` 还没有它 → Go 调新端点
  热更新,**不重启**。

### 网关新增端点

```
POST /v1/providers
请求体: {"name": "moonshot", "base_url": "...", "api_key": "...", "route": "openai", "headers": {...}}
行为:   更新 _provider_conns[name](幂等覆盖);若涉及 registry 缓存则失效重建
响应:   200 {"status": "ok"}
```

仅监听在 `127.0.0.1`(与网关一致),api_key 经 localhost body 传输,可接受。

### Go 客户端

```go
// pkg/llm/client.go
func (c *Client) RegisterProvider(ctx context.Context, name string, conn config.ProviderConn) error
// POST /v1/providers;TUI 新增/修改某 provider 凭据后调用
```

## 8. TUI 配置流(首启向导 + /provider 命令)

复用现有 overlay/选择机制(`pkg/tui/sessionpicker.go`、`menu.go`、`cmdpanel.go`、`commands.go`)。

- **首启**:`prepareAgent` 检测当前 `provider` 是否有可用 key(查 config.providers / credentials / env
  三处)。无 → 弹出 provider 配置向导。
- **向导三步**:
  1. 选 provider(`BuiltinProviders` 列表)
  2. 选 model(拉 `/v1/models`,按该 provider 过滤;catalog 与 litellm 发现合并)
  3. 输入 api_key(及可选 base_url)
  → 写 `credentials.yaml`(`0600`)+ 更新 `config.yaml` 的 `provider`/`model`;
  若是新增 provider 且网关在线,调 `RegisterProvider` 热更新。
- **运行中** `/provider`:打开同一面板,切 provider/model 或补改 key。
  - 切已配 provider 的 model:软切(改内存 cfg + 重算预算),零网关交互。
  - 新增/改 key:写 credentials + `RegisterProvider`。

## 9. main.go 接线

- 首启:当前 provider 无可用 key → 触发向导(在进入主循环前)。
- `LCODER_PROVIDERS` 灌入**全部已配置 provider**(credentials ∪ config.providers),
  而非仅当前 provider,使运行中切到任意已配 provider 零重启。
- 现有 `lookupModelWindow` + `ResolveContextBudget` 在切换 model 后重新调用以更新预算。

## 10. 安全考量

- `credentials.yaml` 权限 `0600`;在 `.gitignore` 增加 `credentials.yaml` 与 `.lcoder/credentials.yaml`。
- `POST /v1/providers` 仅 `127.0.0.1`;不记录 api_key 到日志。
- TUI 输入 key 时做掩码显示(`*`),不回显明文。
- `config.yaml` 不再承载 key,可安全提交。

## 11. 错误处理

- `/v1/models` 拉取失败(网关未就绪/超时):TUI 候选回退到 `models.yaml` catalog;若仍为空,
  允许用户手输 model id。
- `RegisterProvider` 失败:提示用户,回退为热重启网关(方案 B 路径作为兜底),或保留旧 provider。
- registry 解析不到 provider:网关返回 `error`/`bad_request` SSE 事件(复用现有错误通道),
  TUI 提示"未知 model,请在 models.yaml 声明或检查拼写"。
- credentials.yaml 解析失败:警告并忽略该文件(不阻断启动),退回 env/手写 providers。

## 12. 测试策略

- `pkg/config`:credentials 加载/合并/优先级链;`BuiltinProviders` 查找;"当前 provider 是否有可用 key"探测;credentials 写回(临时文件,校验 `0600` 与内容)。
- `pkg/llm`:`RegisterProvider` 经 httptest 校验 `POST /v1/providers` 请求体与错误处理。
- 网关(若有 pytest):`registry.resolve` 三源优先级;`stream_turn` 在 provider 省略时正确解析;
  `POST /v1/providers` 幂等更新。
- `pkg/tui`:providerpanel 选择状态机(选 provider→拉 model→输入 key)的单元测试,
  复用现有 picker 测试模式。

## 13. 改动面清单

1. `gateway/lcoder_gateway/registry.py` — 纳入 `_provider_conns`;新增 `resolve(model_id)`。
2. `gateway/lcoder_gateway/server.py` — `stream_turn` 缺省 provider 解析;新增 `POST /v1/providers`。
3. `pkg/models/message.go` — `ModelRef.Provider` 改 `omitempty` 并注释为可选。
4. `pkg/config/builtin_providers.go`(新) — 内置 provider 表。
5. `pkg/config/credentials.go`(新) — `credentials.yaml` 读写 + 合并进 `cfg.Providers`。
6. `pkg/config/config.go` — `Load()` 接入 credentials 合并;"provider 是否有可用 key"探测辅助。
7. `pkg/llm/client.go` — `RegisterProvider`。
8. `pkg/tui/providerpanel.go`(新) — 配置 overlay。
9. `pkg/tui/commands.go` — 注册 `/provider`(及可选 `/model`)。
10. `cmd/lcoder/main.go` — 首启向导触发;`LCODER_PROVIDERS` 灌入全部 provider;切换后重算预算。
11. `.gitignore` — 增加 credentials 文件;`configs/lcoder.yaml` 与文档同步说明。

## 14. 明确不做(YAGNI)

- OS keyring(本次选定独立文件方案)。
- 把 model 元数据库整体搬进 Go(继续以 `/v1/models` 为准)。
- provider 健康探测 / 多 key 轮换 / 自动 failover。
- per-request 在 TurnRequest body 里携带完整连接覆盖(用启动期灌入 + reload 端点替代)。
