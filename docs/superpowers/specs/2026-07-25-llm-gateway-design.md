# Lcoder LLM Gateway 改善设计(借鉴 kimi-code)

日期:2026-07-25
状态：已确认(方案 A + 四范围 + OpenAI Responses 第三 wire,逐节经用户批准)

## 背景与目标

Lcoder 的 LLM gateway(pkg/llm）架构已与 kimi-code 同代（wire 协议与供应商解耦、models.dev 目录三层合并），但存在四类差距。本设计参考 kimi-code 的 kosong/catalog 机制改善：

1. **能力体系 + 元数据丰富化**:catalog 条目只有 window/output/tools/reasoning/价格；未知模型静默回退 128k window + 空能力；无 modalities、无独立 input 上限、无 status 过滤
2. **wire 推断 + 端点校验**：自定义 provider 的 `route` 要手填；`base_url` 不校验，配错端点时 API key 会被发到错误主机（凭据泄露级问题）;models.dev 的 `npm`/`api` 字段拿到了却没用
3. **thinking effort 语义**:Lcoder 从不在请求里配置 thinking，只被动解析上游发来的 thinking 内容；无法表达档位与"关不掉"的模型
4. **OpenAI Responses 第三 wire**:gpt-5-codex / o-pro 系列只走 Responses API，现有 OpenAICompat 适配器调不到

另加一个小项：`models_source` 配置（自定义 models.dev 风格 registry URL)。**明确不做**：刷新写回 config(kimi-code 的 config.toml 回写，收益不抵复杂度）、Google GenAI 原生 wire、Bedrock/Vertex、TUI effort 档位选择器。

## 已确认的边界条件

- 方案 A:catalog 单包内聚——能力兜底表、wire 推断、端点校验全部进 `pkg/llm/catalog`，不拆新包
- thinking 只在配置层生效（`model.thinking`),TUI 选择器留后续
- 项目开发阶段，无需向后兼容，只求架构整洁
- 配置/参数改动须对齐 test / 生产 / eval 三场景（含 `configs/models.yaml` 与 snapshot 再生）

## §1 catalog 元数据丰富化与过滤

`catalog.Entry` 扩展：

```go
type Entry struct {
    ID, Name, Provider string
    ContextWindow int      // limit.context(总窗口,completion 预算用)
    MaxInput      int      // limit.input(prompt 预算用;0 = 无独立上限,用 ContextWindow)
    MaxOutput     int      // limit.output
    Capabilities  []string // "tools"/"reasoning"(沿用字符串形式,下游零改动)
    Modalities    []string // 输入模态:"image"/"video"/"audio"
    Status        string   // 原始值,仅用于过滤与展示
    Efforts       []string // reasoning_options 解析出的 thinking 档位
    OffEffort     string   // "关 thinking"的线上编码(如 "none");空 = 关不掉
    Cost          struct{ Prompt, Completion, CacheRead, CacheWrite float64 } // 不变
}
```

`fetchModelsDev` 解析新增 `limit.input`、`modalities.input`、`status`、`reasoning_options`。reasoning_options 解析规则（照搬 kimi-code `catalogThinkingOptions`):`{type:"effort", values:[...]}` 取档位；values 中的 `"none"` 或 JSON null → `OffEffort`;`{type:"toggle"}` 记布尔形态（影响 AlwaysThinking 判定，见 §4)。

**入库过滤**（对齐 kimi-code `isUsableChatModel`)，发生在解析层，`merge` 与快照格式不变：

- `status` 为 `deprecated` 或 `alpha` → 丢弃
- id/name/family 含 embedding 标记（子串 `embedding` 或边界词 `embed`)→ 丢弃
- 输出模态声明了但不含 `text` → 丢弃

**snapshot.json 重新生成**：字段扩展后从 models.dev 重新烘焙。生成方式为一次性脚本/命令（手动跑，不进 CI)，脚本与产物一起提交。

**下游消费**:

- 新增 `MaxInput(provider, model) int`，与 `Window`/`MaxOutput` 同款精确+前缀匹配；0 = 未知
- contextmgr 压缩触发线改用 `MaxInput`(0 回退 `Window`)——唯一的消费方改动
- `List()` 的 `models.ModelInfo` 加 `Modalities`/`Status` 字段，供 TUI 面板展示（面板渲染改动最小化，仅展示）

## §2 静态能力兜底表(capability.go)

新文件 `pkg/llm/catalog/capability.go`，结构照搬 kimi-code `capability-registry.ts`，按 Lcoder 的 route 分派：

```go
type FallbackCapability struct {
    Capabilities  []string // tools / reasoning
    Modalities    []string // image / video / audio(与 §1 Entry.Modalities 同语义)
    ContextWindow int      // 0 = 未知
    MaxOutput     int      // 0 = 未知
}

func LookupFallback(route, model string) (FallbackCapability, bool)
```

表内容按 route 分组，组内首条命中即返回，未命中 `ok=false`:

- `anthropic`:`claude-3-`/`claude-3.5-`/`claude-3.7-` → tools + image 模态;`claude-opus-4`/`claude-sonnet-4`/`claude-haiku-4`/`claude-fable` → tools+reasoning + image 模态
- `openai`(含 openai-responses):`^o\d` → tools+reasoning;`gpt-4o`/`gpt-4-turbo`/`gpt-4.1`/`gpt-4.5` → tools + image 模态
- gemini 经 openai-compat：按 model id 前缀 `gemini-` 匹配（2.5 系 → tools+reasoning + image/video/audio 模态）

**查询链改造**:`Window`/`MaxInput`/`MaxOutput`/capabilities/modalities 查询在 catalog 目录未命中时回落 `LookupFallback`;fallback 也未命中才返回 0/空（再往下游才是现有 128k 默认 window)。降级链：目录精确 → 目录前缀 → 静态前缀表 → 默认值。每级独立可测。`MaxInput` 在静态表无对应字段，固定返回 0，由 contextmgr 回退 `Window`。

## §3 wire 推断与端点校验(resolve.go)

新文件 `pkg/llm/catalog/resolve.go`:

```go
type ResolvedProvider struct {
    Route   string // "anthropic" | "openai-responses" | "openai"
    BaseURL string
    Guessed bool   // route 是推断的而非显式声明
}

func ResolveProvider(name string, conn config.ProviderConn, cat *Catalog) (ResolvedProvider, error)
```

规则（对齐 kimi-code `resolveCatalogImport`，裁到三种 wire):

1. `route` 显式给出 → 直接使用，只校验 base URL
2. `route` 为空 → 按 models.dev 记录推断：npm/id 含 `anthropic`/`claude` → `anthropic`；含 `codex` → `openai-responses`；其余 → `openai` 并置 `Guessed=true`（启动时一条 info 日志）；目录查不到的自定义 provider：有 `base_url` → `openai` + Guessed，无 → 报错提示必须给出 `base_url` 或 `route`
3. base URL 校验（显式与推断同走）：为空且无协议默认 → 报错；含 `${` 占位符 → 报错（配置无法表达，防 key 发错主机）;route 为 `anthropic` 时剥掉尾部 `/v1`(models.dev 的 api 字段带 `/v1`，直连会 POST 到 `/v1/v1/messages` 404)
4. base URL 默认值查表按 **route 归属的协议族**查：`openai-responses` 复用 `openai` 的默认 base(二者同 `api.openai.com/v1`)

**接线**:`cmd/lcoder/wiring.go` 的 `buildEngine` 注册连接前逐个 resolve；校验错误 fail-fast 启动失败（配置错误就该 fail-fast，与 hook 的 fail-open 策略不同）。

## §4 thinking effort 语义

**catalog 侧**派生判定（§1 已解析 `Efforts`/`OffEffort`):

```go
// AlwaysThinking: 声明了档位,但无 OffEffort 且无 toggle —— thinking 关不掉。
// anthropic wire 例外:协议级 thinking:{type:"disabled"} 总是可用,不标记。
```

**配置侧**(`pkg/config`):

```yaml
model: claude-sonnet-4-6
thinking: medium   # off | on | <档位>;缺省 = 不发 thinking 字段(保持现状)
```

启动校验：`thinking: off` 而 catalog 判定 AlwaysThinking → warning 并忽略；档位不在 `Efforts` 且非 `on`/`off` → warning 并回落 `on`。

**请求构造**:`models.TurnRequest` 加 `Thinking string` 字段，engine 把 resolved 值传给 adapter,adapter 按 wire 映射：

| wire | on/档位 | off |
|---|---|---|
| openai(chat completions) | `reasoning_effort: <档位>` | 有 OffEffort 发该值；无则省略字段 |
| openai-responses | `reasoning: {effort: "<档位>"}` | 同上 |
| anthropic | `thinking: {type:"enabled", budget_tokens}`,budget = `max(1024, max_output/2)` 且保证 `< max_tokens` | `thinking: {type:"disabled"}` |

## §5 OpenAI Responses 第三 wire

新文件 `pkg/llm/provider/openai_responses.go`，实现现有 `Adapter` 接口（`Stream(ctx, conn, req) → <-chan Event`)。POST `{base}/responses`,`Bearer` 鉴权，SSE 框架复用 openai.go 结构。

**出站映射**(`models.Message` → Responses `input`):

- system → 顶层 `instructions`
- user → `input_text`;assistant → `output_text`
- tool call → `function_call` item(`call_id` 关联）;tool result → `function_call_output`
- 工具定义：Responses 为平铺结构 `{type:"function", name, description, parameters}`（非 chat completions 的嵌套 `function` 对象）
- `max_output_tokens` 替代 `max_completion_tokens`

**入站映射**(SSE → 统一 `Event`):`response.output_text.delta` → 文本；`response.reasoning_summary_text.delta` → thinking;`response.function_call_arguments.delta`/`done` → 工具调用；`response.completed` → usage(`input_tokens`/`output_tokens`);`response.failed`/`error` → 错误事件。全部落到现有 `Event` 类型，engine/agent 零改动。

**注册**:engine 工厂从 if/else 改三分支（`anthropic` / `openai-responses` / 其余 openai 兼容）;route 字符串 `"openai-responses"`；默认 base 复用 openai（见 §3.4)。

**明确不做**(YAGNI，适配器结构不挡路）:`store`/加密 reasoning 回传、`previous_response_id` 链式续接、prompt_cache_key、Responses-only 模型自动切换提示。

## §6 自定义 registry URL

`~/.lcoder/config.yaml` 加 `models_source: <url>`，环境变量 `LCODER_MODELS_SOURCE` 同效且优先；透传到已有的 `catalog.Options.SourceURL`（字段已存在，只缺配置入口）。一行 wiring。

## §7 错误处理与降级

| 场景 | 策略 |
|---|---|
| models.dev 刷新失败 | 静默回落快照/cache（现状不变） |
| 目录与静态表均未命中 | 返回未知（0/空能力），下游走默认值，非致命 |
| resolve 校验失败（空 URL/占位符/缺 route+base) | fail-fast 启动失败 |
| thinking 配置非法 | warning + 降级（off→忽略、未知档位→on) |
| Responses 流中出现未知事件类型 | 跳过继续（向前兼容） |

全程无 panic 路径。

## 包结构（新增/变更）

- `pkg/llm/catalog/catalog.go` — Entry 扩展、过滤、MaxInput、查询链接静态表
- `pkg/llm/catalog/capability.go` — **新增**，静态前缀能力表
- `pkg/llm/catalog/resolve.go` — **新增**,wire 推断 + 端点校验
- `pkg/llm/catalog/snapshot.json` — 重新烘焙（含生成脚本）
- `pkg/llm/provider/openai_responses.go` — **新增**，第三 wire
- `pkg/llm/provider/openai.go` / `anthropic.go` — thinking 字段映射
- `pkg/llm/engine/engine.go` — 工厂三分支
- `pkg/models` — TurnRequest.Thinking、ModelInfo.Modalities/Status
- `pkg/config` — `model.thinking`、`models_source`
- `pkg/contextmgr` — 压缩触发线改用 MaxInput
- `cmd/lcoder/wiring.go` — buildEngine 接 resolve
- `configs/models.yaml` — 对齐三场景（test/生产/eval)

## 测试策略

沿用 `go test $(go list ./... | grep -v 'reference/Shannon')`:

- catalog：新字段解析 fixture(reasoning_options 各形态：effort/toggle/none/null)、过滤规则四分支、MaxInput 匹配链
- capability：每个 route 组命中/未命中、降级链四级各一级
- resolve：显式 route、推断三分支、URL 校验各错误分支、`/v1` 剥离、Guessed 标记
- thinking：配置校验三分支、三个 adapter 的字段映射（provider 测试同构扩展）
- responses 适配器：SSE fixture 流（文本/thinking/工具调用/usage/错误/未知事件各一条）、消息映射含 tool call 往返、engine 工厂分派
- wiring:buildEngine 集成 resolve 的失败路径
- `configs/models.yaml` 与重生 snapshot 的一致性校验（三场景对齐）
