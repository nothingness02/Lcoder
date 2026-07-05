# Go LLM Gateway 设计(替换 Python FastAPI+litellm)

> 状态:已批准设计,待写实现 plan
> 日期:2026-06-28

## 背景与目标

当前 Lcoder 是两进程架构:Go agent/TUI ↔ HTTP/SSE ↔ Python FastAPI + litellm 网关
(`gateway/lcoder_gateway/`,约 1054 行)。Go 侧通过 `pkg/llm/gateway.go` 的 `StartGateway`
spawn Python 子进程,经 4 个 HTTP 端点(`/v1/turn` SSE、`/v1/models`、`/v1/providers`、`/v1/health`)通信。

**目标**:用**进程内 Go 引擎**替换 Python 网关,实现单二进制分发(下完即跑,无需 Python 环境 / pip / 子进程生命周期)。

**核心收益**:单二进制分发;冷启动更快;去掉内部一跳序列化;类型单一真相源;去掉 litellm 巨型依赖。

**代价**:Anthropic / Gemini 非 OpenAI 兼容,需要手写 adapter(higress `ai-proxy/provider` 提供参考实现)。

## 已定架构决策

1. **网关形态:内嵌库**。彻底去进程边界——agent loop 直接调进程内 Go 接口,provider adapter 同进程。无 HTTP/SSE 内部跳、无 subprocess。
2. **provider 覆盖:2 个 adapter 覆盖 6 家**。OpenAI-compat 覆盖 openai/deepseek/moonshot/openrouter + Gemini(走其 OpenAI 兼容端点);Anthropic 一个独立 adapter。
3. **adapter 实现:手写 HTTP + SSE 解析**(像 higress 那样,最小依赖,仅标准库 net/http)。
4. **模型目录:models.dev 快照 + 刷新**。二进制内置快照(离线可用),后台非阻塞刷新,models.yaml 用户覆盖。
5. **切换策略:建完再一次性删 Python**。Go engine 连同所有测试写完、6 家实调冒烟通过后,一次性删除 Python 网关。

## 总体数据流(前后对比)

```
现在:  agent loop → llm.Client.StreamTurnRetry → HTTP POST /v1/turn
        → Python server.py → litellm.acompletion → provider 原生 API
        → SSE 回流 → client.go 解析 → TurnStream.Next()

之后:  agent loop → llm.Client.StreamTurnRetry → engine.StreamTurn (进程内)
        → 按 provider.Route 选 adapter → adapter 手写 HTTP → provider 原生 API
        → adapter 解析 SSE → chan Event → TurnStream.Next()
```

进程边界、subprocess、内部那段 SSE 全部消失。`TurnStream.Next()` 的消费形态不变,底层从
`io.ReadCloser` 改成 channel。

## 包结构(镜像 higress `ai-proxy/provider` 布局)

```
pkg/llm/
  client.go          [改] 保留方法面,内部委托给 engine
  engine/
    engine.go        [新] 核心编排:路由 → cache policy → adapter → 归一化 → 算成本
  provider/
    event.go         [新] Event 类型(start/text_delta/thinking_delta/toolcall_delta/done/error)
    adapter.go       [新] Adapter 接口 + Conn + 各家 base_url 默认表
    openai.go        [新] OpenAI-compat:覆盖 openai/deepseek/moonshot/openrouter/gemini
    anthropic.go     [新] Anthropic Messages API
  catalog/
    catalog.go       [新] models.dev 快照 + 刷新 + models.yaml 覆盖
    snapshot.json    [新] go:embed 内置快照(离线兜底)
  pricing.go         [新] estimate_cost 移植(价格来自 catalog 条目)
  cachepolicy.go     [新] apply_cache_policy 移植(仅 Anthropic)
```

## 组件设计

### Client 接缝(最小改动)

`llm.Client` 五个方法保留签名,只换内部实现:

| 方法 | 现在 | 之后 |
|---|---|---|
| `StreamTurn` | HTTP POST + SSE 解析 | `engine.StreamTurn` → channel-backed `TurnStream` |
| `ListModels` | GET /v1/models | `catalog.List()` |
| `ModelWindow` | 从 ListModels 找 | `catalog.Window(provider, model)` |
| `RegisterProvider` | POST /v1/providers | `engine.RegisterProvider`(进程内 map) |
| `Health` | GET /v1/health | 直接返回 `{"status":"ok"}` |

`StreamTurnRetry`(retry.go)、agent loop 的 `.Next()` 循环、TUI providerpanel 的调用**全部不动**。
`TurnStream` 从包装 `io.ReadCloser` 改为包装 `<-chan GatewayEvent`,`Next()` 改为从 channel 读;
事件名/payload 形状与现在完全一致(start/text_delta/thinking_delta/toolcall_delta/done/error)。

### Engine(`engine/engine.go`)

持有:
- provider 连接注册表(进程内 map,来自 `RegisterProvider`)
- catalog(models.dev)
- pricing 表(来自 catalog 条目)
- cache policy

`StreamTurn(ctx, req) (<-chan Event, error)` 流程:
1. 解析 provider(显式 `req.Model.Provider`,否则从 catalog 反查)
2. 按 `provider.Route` 选 adapter
3. (Anthropic)应用 cache policy
4. 调 `adapter.Stream` 获得归一化 Event channel
5. 透传 Event;在 `done` 时用 catalog 价格算 `LLMUsage` 成本字段
6. 错误分类为 `bad_request/auth/rate_limit/internal`(对齐现有 GatewayError code)

### Adapter 接口(`provider/adapter.go`)

```go
type Adapter interface {
    // Stream 发起一次 provider 调用,把归一化后的 Event 投进 channel。
    Stream(ctx context.Context, conn Conn, req models.TurnRequest) (<-chan Event, error)
}

type Conn struct {
    BaseURL string            // 缺省走各家默认表
    APIKey  string
    Headers map[string]string
}
```

**包依赖方向(避免循环)**:`Event` 定义在 leaf 包 `provider`(`provider/event.go`),adapter 定义并产出
它,`provider` 不 import `engine`;`engine` 单向 import `provider`。`engine.StreamTurn` 返回的
channel 元素即 `provider.Event`,`client.go` 再把它映射成现有 `GatewayEvent` 形状喂给 `TurnStream`。

引擎按 `provider.Route` 选 adapter:`openai`/`openrouter`/自定义 → OpenAICompat;
`anthropic` → Anthropic;`gemini` → OpenAICompat 指向 Gemini 的 OpenAI 兼容端点。

### OpenAI-compat adapter(`provider/openai.go`)

各家默认 base_url:

| provider | base_url | 鉴权 |
|---|---|---|
| openai | `https://api.openai.com/v1` | `Authorization: Bearer` |
| deepseek | `https://api.deepseek.com/v1` | Bearer |
| moonshot | `https://api.moonshot.cn/v1` | Bearer |
| openrouter | `https://openrouter.ai/api/v1` | Bearer |
| gemini | `https://generativelanguage.googleapis.com/v1beta/openai` | Bearer |

**请求**:`POST {base}/chat/completions`,标准 OpenAI body(`messages`/`tools`/`stream:true`/
`temperature`/`max_tokens`/`top_p`)。消息转换复用现有 `message_to_litellm` 的等价 Go 逻辑
(system / user / assistant+tool_calls / tool)。

**SSE 解析**:逐行读 `data: {json}`,`data: [DONE]` 收尾。对每个 chunk 取 `choices[0].delta`:
- `delta.content` → `text_delta`
- `delta.reasoning_content`(DeepSeek reasoner)→ `thinking_delta`
- `delta.tool_calls[]`(带 `index`,流式分片累积 `function.arguments`)→ `toolcall_delta`
- 末尾 chunk 的 `usage` → 提取 token

### Anthropic adapter(`provider/anthropic.go`)

**请求**:`POST https://api.anthropic.com/v1/messages`,头 `x-api-key` + `anthropic-version: 2023-06-01`。
body 用 Anthropic 原生格式:顶层 `system`(可带 `cache_control`)、`messages`、`tools`(`input_schema`)、
`max_tokens`、`stream:true`。

**SSE 解析**(事件类型与 OpenAI 完全不同):
- `message_start` → 起始,读初始 usage
- `content_block_start`(type=`tool_use`)→ 新工具调用,记 id/name
- `content_block_delta`:`text_delta`→text;`thinking_delta`→thinking;`input_json_delta`→工具参数分片(`toolcall_delta`)
- `content_block_stop` / `message_delta`(带最终 usage:`input_tokens`/`output_tokens`/
  `cache_creation_input_tokens`/`cache_read_input_tokens`)
- `message_stop` → done

参考 `reference/higress/.../ai-proxy/provider/claude.go` 与 `claude_to_openai.go`(Apache 2.0,照着写不直接拷)。

### Catalog(`catalog/catalog.go`)—— 取代 registry.py + catalog.py

三层合并,优先级**用户 models.yaml > models.dev > 内置快照**:
1. **内置快照** `snapshot.json`(`go:embed`,离线兜底)——从 models.dev api.json 裁出 6 家所需字段
2. **models.dev 刷新**:fetch `https://models.dev/api.json` → 缓存 `~/.lcoder/cache/models.json`,
   TTL 5 分钟;**完全非阻塞**——启动立即用快照,刷新在后台 goroutine 异步进行,永不卡启动;
   失败回退快照。config 可关(离线/隐私)。
3. **models.yaml 覆盖**:复用现有 `config.ModelCatalog`,给自定义/本地模型留声明途径

每条目带:`id` / `name`(展示名)/ `provider` / `context_window` / `capabilities` /
`cost{input,output,cache_read,cache_write}`。

`models.ModelInfo` **新增 `Name string` 字段**(展示规范命名)。

### Pricing(`pricing.go`)—— 移植 estimate_cost

逻辑照搬:`tokens × price_per_1M / 1e6`,四档(prompt/completion/cache_read/cache_write)。
价格来自 catalog 条目的 `cost`;未知模型返回 0(与现状一致)。引擎在 `done` 时算好填进 `LLMUsage`。

### Cache policy(`cachepolicy.go`)—— 移植 apply_cache_policy

**仅 Anthropic**:在 Anthropic adapter 内部,按 `req.CacheBreakpoints` 给对应消息的 text block 打
`cache_control:{type:ephemeral}`;system prompt 和最后一个 tool 定义也打;无 breakpoints 时兜底给
最后一条 user 消息打。OpenAI-compat 一侧忽略(DeepSeek/OpenAI 服务端自动缓存,无需客户端标记)。

## 错误处理

adapter 把 provider HTTP 状态码 / 错误体分类为现有 `GatewayError.Code`:
- 4xx 鉴权 → `auth`
- 400 其他 → `bad_request`
- 429 → `rate_limit`
- 其余 / 传输错误 → `internal`

经 `error` 事件回流,`GatewayError` 结构与字段(`code`/`message`/`provider_error`)不变,
agent loop 的错误处理零改动。

## 切换与删除(一次性)

构建 + 测试全绿、6 家实调冒烟通过后,一次删除:
- 整个 `gateway/` 目录(Python + pytest + pyproject)
- `pkg/llm/gateway.go`(StartGateway / GatewayManager)
- `client.go` 的 HTTP transport 与 SSE 解析
- `main.go` 的 subprocess 生命周期 / `ensureGateway` / `waitForGateway` / `--gateway-cmd` 相关配置

## 测试策略(TDD,与现有 pytest 行为对齐)

- **adapter 单测**:`httptest` 起 server 喂罐装 provider SSE → 断言归一化 Event。
  **必须覆盖之前的 bug**:流式 tool_call 分片(toolcall_delta)。逐条对照现有 29 个 gateway
  pytest 用例搬成 Go。
- **engine 单测**:provider 路由、cache policy 注入、成本计算、错误分类。
- **catalog 单测**:快照加载、models.yaml 覆盖、刷新 fetch(httptest)、刷新失败回退快照。
- **回归**:agent/loop 既有测试保持绿(Client 方法面没变)。
- **人工冒烟**:删 Python 前,6 家各真调一次。
- 测试中涉及 HOME 的用例设 `HOME`/`USERPROFILE` 到临时目录;用 `go test ./pkg/... ./cmd/...`。

## 验证标准

1. `go build ./...`;`go test ./pkg/... ./cmd/...` 全绿;`go vet`(本次包);`gofmt -l`(本次文件)。
2. adapter 测试覆盖:text/thinking/toolcall(含流式分片)/usage/error 五类事件。
3. 6 家真实 provider 各冒烟一次(流式回字、工具调用、成本显示正确)。
4. 启动无 Python、无子进程;`ls gateway/` 不存在;二进制独立可跑。

## 明确不做(YAGNI)

- 不做 higress 那 40 家 provider——只 6 家
- 不做 Gemini 原生 adapter(走 OpenAI 兼容端点)
- 不做 bedrock / vertex / azure
- 不保留 Python 双跑开关(选了一次性删)
- 不引入官方 SDK(手写 HTTP)
- 不做运行时预算热刷新 / 网关重连(进程内无此概念)
