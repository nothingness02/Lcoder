# LLM Gateway 机制文档

> 本文描述 Lcoder 的 in-process LLM gateway(`pkg/llm`)的原理、运行逻辑、核心调用路径与数据流。
> 读者：需要理解或修改 LLM 链路的维护者。代码基线:master @ 2026-08（含三批弹性改造)。

---

## 1. 定位与分层

Lcoder 的 LLM gateway **不是独立代理进程**，而是一组 in-process Go 包。agent loop 只面向 `llm.Client`，其余全部是内部实现。

```
┌────────────────────────────────────────────────────────────────┐
│ 消费层  pkg/agent/streamer.go   事件消费 + 第 2 层重试(整轮)     │
├────────────────────────────────────────────────────────────────┤
│ 门面层  pkg/llm/client.go       薄封装 + 第 1 层重试(建流)      │
│         pkg/llm/retry.go        IsRetryable / Backoff           │
├────────────────────────────────────────────────────────────────┤
│ 路由层  pkg/llm/engine          provider 注册表、credPool、      │
│                                 并发闸口、adapter 分派、计费转发 │
├────────────────────────────────────────────────────────────────┤
│ 协议层  pkg/llm/provider        3 个 wire adapter + 错误分类     │
├────────────────────────────────────────────────────────────────┤
│ 目录层  pkg/llm/catalog         模型元数据(窗口/价格/thinking)   │
├────────────────────────────────────────────────────────────────┤
│ 组装层  cmd/lcoder/wiring.go    buildEngine:配置 → 引擎         │
└────────────────────────────────────────────────────────────────┘
```

依赖方向只允许向下：`agent → llm → engine → provider / catalog`。目录层不依赖任何上层。

---

## 2. 配置解析（启动路径)

### 2.1 配置来源与优先级

provider 连接信息有三个来源，**字段级**合并（高覆盖低）:

1. `~/.lcoder/config.yaml` 手写的 `providers:` 段（最高优先）
2. `~/.lcoder/credentials.yaml`(TUI provider 向导写入,`mergeCredentials` 只填空缺字段)
3. 环境变量（`pkg/config/builtin_providers.go` 的 `KeyEnv` 映射,如 deepseek → `DEEPSEEK_API_KEY`)

`{env:VAR}` 占位符在加载期展开。

### 2.2 ProviderConn 字段

`pkg/config/providers.go`:

| 字段 | 语义 | 缺省行为 |
|------|------|---------|
| `base_url` | 端点 | 按 provider 名/route 查默认表 |
| `api_key` | 单凭证 | — |
| `api_keys` | failover 凭证池 | 空 = 单 key,无池 |
| `route` | provider 名(查默认 base URL、catalog 元数据) | 由 catalog 推断 |
| `protocol` | 线协议:`openai-chat`/`openai-responses`/`anthropic` | 空 = 由 route 派生;**非法值启动报错,绝不沉默兜底** |
| `headers` | 自定义头(最后合并) | — |
| `max_concurrent` | 并发流上限 | 0 = 不限 |

### 2.3 配置示例:自建代理端点

```yaml
providers:
  claude-proxy:
    base_url: "http://localhost:4000/v1"
    api_key: "{env:PROXY_KEY}"
    protocol: anthropic   # 显式声明线协议;不写则由 route 派生
    route: openai         # 随意,仅用于默认 base URL 和 catalog 元数据查询;
                          # base_url 已显式给出时,它只剩 catalog 查询一个作用
```

要点:`base_url` + `protocol` 是自建端点的关键两字段;`route` 填不填、填什么只影响 catalog 元数据(窗口/价格)查询——填一个线上协议相近的已知 provider 名(如 `openai`)可以白嫖它的目录数据,乱填的代价只是元数据查不到(窗口返回 0,走 contextmgr 默认值),不会发错请求。

**配错 protocol 的两种命运**:值本身非法(不在三枚举内)→ 启动直接报错(TUI 热注册则返回 error 显示);值合法但选错(如该 anthropic 写了 openai-chat)→ 用错误协议格式打过去,运行期 400。

### 2.4 buildEngine 装配

`cmd/lcoder/wiring.go: buildEngine()`:

1. 建 catalog:内置 snapshot →（后台）models.dev → 用户 models.yaml override
2. 对每个 provider 调 `catalog.ResolveProvider(name, route, protocol, baseURL)`:
   - route 缺省 → `inferRoute`(anthropic/claude 名 → anthropic;codex → openai-responses;其余 → openai),并打 `info:` 日志
   - **protocol 显式校验**(`ParseProtocol`),非法直接启动失败
   - baseURL 缺省 → catalog 的 `api` 字段 → 内置默认表;空串/`${}` 占位符 → 拒绝(防止把 key 发到错误主机,凭证泄漏防线)
3. `engine.RegisterProvider(name, provider.Conn{...})`,同时建立:
   - **credPool**(`api_keys` 非空时)
   - **并发信号量**(`max_concurrent > 0` 时)

---

## 3. 目录层:catalog

### 3.1 数据与合并

三层数据按优先级合并（后面的覆盖前面的；override 最后 re-assert，永远赢）:

```
内置 snapshot.json (embed,启动即生效,离线兜底)
  ← merge ← models.dev 数据(启动轮可读 5 分钟内的本地 cache 落盘,
             之后周期直连网络;cache 只是这一层的加速,不是独立一层)
  ← merge ← models.yaml (用户 override)
```

每条 `Entry` 携带:`ContextWindow`、`MaxInput`、`MaxOutput`、`Cost`(prompt/completion/cache_read/cache_write)、`Capabilities`、`Efforts`(thinking 档位)、`OffEffort`、`ThinkingToggle`。

查找细节:provider 名先过小写归一化的别名表(`moonshot→moonshotai`、`gemini→google`、`zai→zhipuai`);model ID 支持双向前缀匹配。

### 3.2 周期刷新与状态

- 启动轮:cache 文件(`~/.lcoder/cache/models.json`)5 分钟内新鲜则直接用,免网络
- 之后 ticker 周期刷新(默认 1h,`Options.RefreshInterval` 可调),**绕过 cache 直接走网络**,成功后回写 cache
- 每轮结果记录在 `Status() (time.Time, error)`——刷新失败不再静默,可通过 §7 的网关 Status 观察
- `Close()` 停止循环(幂等)

### 3.3 查询接口(运行期被谁用)

| 方法 | 调用方 | 用途 |
|------|--------|------|
| `Window(prov, model)` | contextmgr | 上下文预算/compaction |
| `MaxOutput` / `MaxInput` | contextmgr / engine | max_tokens 解析 |
| `PriceTable()` | engine.forward | 计费 |
| `ThinkingSpec()` | engine | thinking 配置校验、off 编码 |
| `ResolveProvider()` | wiring | 启动装配 |

---

## 4. 协议层:provider adapters

### 4.1 三个协议族,Protocol 是一等概念

| Protocol | adapter | 对应 API |
|----------|---------|---------|
| `anthropic` | `Anthropic{Marks}` | Messages API(显式 cache_control) |
| `openai-chat` | `OpenAICompat{}` | Chat Completions(openai/deepseek/gemini/xai/alibaba/zhipu/xiaomi/openrouter/moonshot/kimi-code…) |
| `openai-responses` | `OpenAIResponses{}` | Responses API(codex 类) |

`Protocol` 与 `Route` 是**刻意分开的两个概念**(第三批改造):
- `Route` = provider 名,用于查默认 base URL 和 catalog 元数据
- `Protocol` = 线协议,**唯一决定**用哪个 adapter。engine factory 按 Protocol 穷举分派,无 default 兜底
- 派生规则:显式 `protocol:` 配置 > route 派生(`anthropic`→anthropic,`openai-responses`→openai-responses,其他一切 provider 名 → openai-chat)

### 4.2 adapter.Stream 的两段式结构

```
第一段(同步,决定可重试性):
  构请求体 → json.Marshal → http.NewRequestWithContext
  → doStreamRequest:
      client.Do → 非 200:读 body(≤64KB)→ classifyHTTP → 同步返回 *EventError
      200:返回 resp
第二段(goroutine,流式):
  emit KindStart → bufio.Scanner 逐行解 SSE(1MB 行上限)
  → 归一化 provider.Event 流
  → KindDone(组装好的完整 assistant 消息 + usage)
```

关键点:

- **HTTP 状态码在建流阶段同步检查**(第一批修复)。这是 429/5xx 能被重试层看见的前提——如果错误经 channel 传递,`StreamTurnRetry` 永远看不到它
- **OpenAI 请求强制注入 `stream_options: {include_usage: true}`**——否则官方 API 不回 usage,成本统计和 `RecordRealUsage` 反馈链全为 0
- 所有事件发送走 `emit(ctx, ...)`:ctx 取消时不再阻塞(防 goroutine 泄漏)
- SSE 错误帧翻译成 KindError:Anthropic `{"type":"error"}`(`overloaded_error`/`rate_limit_error` → rate_limit code);Responses `response.failed`/`error`
- 请求/响应模型转换在 `convert.go`/`anthropic.go`/`openai_responses.go` 内部完成,对外只有统一的 `provider.Event`

### 4.3 错误分类(classifyHTTP)

| 条件 | Code | IsRetryable |
|------|------|-------------|
| 429 | `rate_limit` | 是 |
| 401/403 | `auth` | 否 |
| 400 + body 含 `context_length_exceeded`/"prompt is too long"/"maximum context length" | `context_overflow` | **否**(语义上应走 compaction) |
| 400 其他 | `bad_request` | 否 |
| 5xx/其他 | `internal` | 是 |

同时解析 `Retry-After`(秒数或 HTTP-date)与 `Retry-After-Ms` 头 → `EventError.RetryAfter`,供退避策略优先采用。

> **`context_overflow` 的诚实边界**:目前**只有归类,没有消费者**——agent loop 收到它后与其他不可重试错误一样走终局(发 ErrorEvent、run 结束)。"应走 compaction"是预留语义,自动触发 compaction 重建请求的闭环尚未实现(见 §10)。adapter.go 里"the agent layer routes it to compaction"的注释描述的是意图而非现状。

---

## 5. 路由层:engine

### 5.1 StreamTurn 主流程

`pkg/llm/engine/engine.go: StreamTurn(ctx, req)`:

```
1. resolveProvider(req.Model)           provider 名(缺省按 model ID 反查目录)
2. RLock 取 Conn                        providers map 有 RWMutex 保护
3. Protocol 确定(显式 > route 派生)
4. selectCredential(prov)               failover 池轮换(有池时覆盖 conn.APIKey)
5. ComputeCacheMarks(...)               anthropic 才需要显式标记
6. ResolveBaseURL / ThinkingOffEffort
7. newAdapter(proto, marks)             按 Protocol 分派
8. 并发闸口获取(有 sem 时,可被 ctx 取消)
9. adapter.Stream(ctx, conn, req)
     ├─ 失败(建流阶段)→ reportCredential(失败)→ 释放闸口 → 返回 err
     └─ 成功 → reportCredential(成功)
10. go forward(ctx, prov, model, src, out, sem)
```

### 5.2 failover 状态机(credPool)

- 每个 provider 的 key 列表按 **round-robin** 轮换
- **建流失败**(同步返回的错误)计入该 key 的连续失败计数;**连续 3 次失败 → 摘除**,冷却 60s 后自动恢复资格(免主动探测;阈值与冷却时长目前是 engine 默认值,未暴露到 yaml)
- 建流成功重置该 key 的失败计数
- 全部被摘除时退化为无视状态轮询(**不放空流量**)
- 与重试的联动:`StreamTurnRetry` 每次 attempt 都重新进 StreamTurn → 自然换下一个 key
- **只统计建流阶段失败**:流内 rate_limit(如 Anthropic overloaded 帧)不摘除 key(见 §10)

### 5.3 并发闸口(sems)

- `max_concurrent > 0` 时,engine 为该 provider 建 `chan struct{}`(cap = max_concurrent)
- StreamTurn 第 8 步获取槽位(`select ctx.Done` 可取消);**forward 退出时释放**
- `len(sem)` 即当前 in-flight 数,直接用于 Status

### 5.4 forward:计费转发 goroutine

逐事件从 adapter channel 搬到调用方 channel:

- `KindDone` 且带 usage:盖上 provider/model,用目录价格表算 `PromptCost`/`CompletionCost`/`CacheReadCost`/`CacheWriteCost`/`TotalCost`
- **收发两侧都 select ctx.Done**:消费者放弃读取(turn 取消)时 forward 立即退出,释放闸口、关闭 out——两端都不泄漏 goroutine

---

## 6. 两层重试(核心决策逻辑)

系统里有两层重试,**按错误发生的阶段分工**(不是调用关系上的隔离——第 2 层每轮重试都会重新走一遍第 1 层)。分工规则保证同一个错误只被一层处理;最坏情况下建流尝试上限为 3×3(第 2 层 3 轮 × 每轮第 1 层 3 次),且第 1 层一旦整轮耗尽立即终局,不会再进第 2 层:

```
streamer.stream()
  │
  ├─ for attempt in 0..MaxAttempts-1:            ← 第 2 层(整轮)
  │    │
  │    ├─ StreamTurnRetry():                     ← 第 1 层(建流)
  │    │    for attempt in 0..MaxAttempts-1:
  │    │      StreamTurn() ─ 成功 → 返回 stream
  │    │      失败 → IsRetryable? → Backoff 等待 → 再来
  │    │    全部失败 → 上抛(streamer 不再重试,直接失败)
  │    │
  │    └─ consume(stream):
  │         正常 KindDone → 返回消息 ✓
  │         KindError 且未流出任何内容(gotContent=false)且可重试
  │           → Backoff 等待 → 下一轮 attempt(重新建流)
  │         KindError 但已流出内容 → 上抛(绝不重试)
```

### 第 1 层:传输层 `StreamTurnRetry`(pkg/llm/retry.go)

- **只管建流阶段**(还没有任何内容流出之前)。由于 HTTP 错误同步返回,429/5xx/auth/400 都在此可见
- 每次 attempt 是一个全新的 StreamTurn(自动轮换 failover key、重新过闸口)

### 第 2 层:会话层 streamer 整轮重试(pkg/agent/streamer.go)

- 只管"**建流成功、但未流出任何内容就收到流内错误**"——典型:流刚建立,供应商发来一帧 `overloaded_error`
- `gotContent` = 是否收到过任何 **delta**(text/thinking/tool-call)。已流出内容 → partial 已 emit 给 UI,重跑会重复输出,**绝不重试**
- **KindStart 不算内容**:所有内置 adapter 在拿到 200 后、读第一帧 SSE 前就 emit KindStart,若把它算作内容,pre-content 重试在真实链路上永不可达(任何流内错误到达时 gotContent 必为 true)。因此 MessageStart 事件推迟到首个 delta(或 Done)才 emit——pre-content 失败重试不会在 UI 留下任何痕迹

### 退避策略 `Backoff(rc, attempt, retryAfter)`

1. 供应商给了 `Retry-After` → **直接采用**(硬上限 2 分钟)
2. 否则 `BaseBackoff << attempt`,**±25% jitter**(防并发 subagent 同步重试),上限 `MaxBackoff`(默认 32s)
3. 等待全程可被 ctx 打断

默认配置:`MaxAttempts=3`(总尝试次数,即首次 + 最多 2 次重试), `BaseBackoff=1s`, `MaxBackoff=32s`。`RetryConfig` 是调用点参数(streamer 固定用 `DefaultRetryConfig()`),未暴露到 yaml。

### 错误终局

两层都耗尽后,错误上抛到 agent loop:发 `ErrorEvent`,`endReason = EndReasonError`,run 结束。**`Prompt()` 的返回值不携带流错误**——签名上有 error 返回,但它只覆盖运行前失败(如 BuildTurnRequest)和中断类错误;流错误一律经事件总线(ErrorEvent)和结束原因观察,`Prompt()` 此时返回 nil。

---

## 7. 观测出口

> **消费者现状**:`Client.Status()` 与 `LLMRetryEvent` 目前都是"只生产、无 UI 消费"——Status 无调用方,retry 事件只有 emit(cmd/lcoder 接到事件总线)但没有 TUI/CLI 订阅展示。属预留出口,供后续状态栏/调试命令接入。

### 7.1 结构化状态 `Client.Status()`

替换了原来的假 `Health()`。快照内容:

```go
Status{
  Providers: map[string]ProviderStatus{
    Route, Protocol,
    Credentials,   // failover 池大小(0 = 单 key)
    Available,     // 未被摘除的 key 数
    MaxConcurrent, // 闸口容量
    InFlight,      // 当前占用(len(sem))
  },
  CatalogLastRefreshAt, CatalogLastError,
}
```

### 7.2 重试事件 `events.LLMRetryEvent`

```json
{"type":"llm_retry", "layer":"establish|turn", "attempt":1, "wait_ms":150, "err":"..."}
```

- 第 1 层:`Client.OnRetry` 回调,`cmd/lcoder/main.go` 接到事件总线
- 第 2 层:streamer 用自己的 emitter 直接发

---

## 8. 核心调用路径

### 8.1 启动路径

```
main.go prepareAgent
  └─ buildEngine(cfg)
       ├─ catalog.New(snapshot + refreshLoop + overrides)
       └─ for each cfg.Providers:
            catalog.ResolveProvider → engine.RegisterProvider(建 credPool + sem)
  └─ llm.NewClient(eng)
  └─ llmClient.OnRetry = bus.Emit(LLMRetryEvent)
  └─ agent = NewWithObservability(cfg, llmClient, ...)
```

### 8.2 一个 turn 的完整路径(数据流)

```
用户输入 → loop.go
  └─ streamer.stream(ctx, turn, modelRef, tools)
       ├─ contextmgr.BuildTurnRequest(modelRef, tools)
       │    → TurnRequest{Messages, SystemPrompt, CacheBreakpoints,
       │                   Thinking, Generation{MaxTokens,...}}
       ├─ (可选) TransformContext
       └─ 重试循环(§6)
            └─ Client.StreamTurnRetry → engine.StreamTurn (§5.1)
                 └─ adapter.Stream (§4.2)
                      │  HTTP+SSE
                      ▼
                 provider.Event 流 ──► engine.forward(计费,§5.4)
                      ▼
                 streamer.consume:
                   delta → 更新 partial + MessageUpdate 事件(首内容记 TTFT)
                   KindDone → MessageEnd 事件
                            → obs.RecordLLMUsage
                            → contextmgr.RecordRealUsage(真实 token 回喂预算)
       └─ 返回 assistant message
  └─ loop: appendMessage → 执行 tool calls → 下一 turn
```

**数据流三条支线**:

1. **请求数据**:messages/tools 由 contextmgr 按 token 预算组织;cache breakpoints 由 contextmgr 计算、engine 转成 provider 标记(仅 anthropic 线上可见);thinking 值经 `ResolveThinking` 校验后由 adapter 映射到各协议字段
2. **事件流**:adapter → forward → streamer →(emitter)→ 事件总线 → TUI/session 持久化/observability 三个订阅方
3. **用量流**:SSE chunk 里的 usage → forward 算成本 → streamer 记账 + 回喂 context manager(后续 turn 的预算决策用真实 token 而非估算)

### 8.3 取消路径

```
用户取消/abort
  → streamCtx cancel
  → HTTP request ctx 取消 → 连接断开 → adapter scanner 报错退出
     → defer: close(事件 channel)+ resp.Body.Close()
  → engine.forward 收发两侧 select ctx.Done → 退出 → 释放闸口 → close(out)
  → 任何进行中的重试等待(两层都是 timer+select)立即中断
  → compaction 的 SummarizeFunc 同样吃 ctx,摘要也可取消
```

### 8.4 TUI 热注册路径

```
TUI provider 向导保存
  → credentials.yaml 持久化
  → Client.RegisterProvider(config.ProviderConn)
       (protocol 显式值在此校验,非法直接返回 error 给 TUI 显示)
  → engine.RegisterProvider(写锁)
  → 与正在进行的 StreamTurn(读锁)并发安全
```

---

## 9. 心智模型速查

- **数据流经的四个环节都是无状态查表**:配置 → 目录 → 路由 → 协议适配。"活"的逻辑只有三处:错误分类(决定能否重试/等多久)、两层重试决策、failover/闸口状态机
- **可重试性的分水岭是"内容是否流出"**:建流失败找第 1 层;流内 pre-content 失败找第 2 层;流出内容后失败认栽
- **Protocol 选 adapter,Route 查元数据**,两者不要再混用
- **取消是全程贯通的**:HTTP → scanner → emit → forward → 重试等待 → 摘要,一条 ctx 链到底
- **usage 是闭环的**:provider → 计费 + 回喂 context 预算,不是单纯展示数据

---

## 10. 当前已知边界

1. 流中途断开(已流出部分内容后)不重试——参考项目会整轮重跑作废 partial,Lcoder 选择保守(不重复输出)
2. `finish_reason`/`stop_reason` 未解析,max_tokens 截断与正常结束在事件层不可区分
3. 无 SSE per-chunk 读超时看门狗(服务端保持连接但停发数据时,只能靠用户取消)
4. failover 只统计建流阶段失败;流内 rate_limit(如 Anthropic overloaded 帧)不摘除 key
5. 无跨 provider fallback 链(刻意不做:流式输出开始后不可切换,复杂度高于收益)
