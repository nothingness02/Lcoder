# Lcoder 开发者快速上手指南

> 本文档面向希望扩展、调试或贡献 Lcoder 的开发者。如果你是普通用户，请阅读 `USER_GUIDE.md`。

## 目录

1. [架构概览](#1-架构概览)
2. [开发环境搭建](#2-开发环境搭建)
3. [核心包导览](#3-核心包导览)
4. [如何编写自定义 Go 工具扩展](#4-如何编写自定义-go-工具扩展)
5. [如何编写 before-tool-call Hook](#5-如何编写-before-tool-call-hook)
6. [如何编写自定义 Observability Exporter](#6-如何编写自定义-observability-exporter)
7. [如何添加 Agent Mode](#7-如何添加-agent-mode)
8. [如何接入 MCP 服务器](#8-如何接入-mcp-服务器)
9. [子代理扩展](#9-子代理扩展)
10. [测试与调试](#10-测试与调试)
11. [贡献指南与代码约定](#11-贡献指南与代码约定)
12. [扩展 API 速查](#12-扩展-api-速查)

---

## 1. 架构概览

Lcoder 的入口在 `cmd/lcoder/main.go`。启动流程可以概括为两条主线：

1. **装配线**（`prepareAgent`）：把配置、LLM 客户端、工具注册表、MCP 注册表、会话存储、可观测性、模式管理器、上下文管理器装配成一个 `agent.Agent`。
2. **运行分发**（`runRoot`）：根据用户输入选择单次对话（one-shot）、JSON 事件流或 TUI 模式，并在 SIGINT/SIGTERM 时写入 crash checkpoint。

### 1.1 核心组件关系

```
cmd/lcoder/main.go
 └─ prepareAgent
     ├─ config.Load()              加载 ~/.lcoder/config.yaml
     ├─ llm.NewClient(engine)      创建 LLM 客户端
     ├─ tools.NewRegistry(...)     创建工具注册表
     ├─ registry.RegisterBuiltinFactories 注册内置工具
     ├─ registry.Register + LoadExtensions 注册 HTTP 扩展工具
     ├─ mcp.NewRegistry(...)       连接 MCP 服务器并注册其工具
     ├─ session.NewStore(...)      创建/加载会话
     ├─ observability.NewCollectorWithAudit 创建可观测性收集器
     ├─ agent.NewModeManager(...)  加载内置与自定义模式
     ├─ agentsetup.NewContextManager 创建上下文管理器
     └─ agent.NewBuilder().Build() 构建 Agent
```

### 1.2 Agent 内部结构

`pkg/agent` 包把核心工作拆成三个内部组件：

- `streamer`（`streamer.go`）：调用 `contextmgr.Manager.BuildTurnRequest` 构造每轮请求，流式接收 LLM 事件，组装 assistant 消息。
- `executor`（`executor.go`）：验证参数、权限检查、执行工具调用，负责 deferred tool 提升。
- `stateHolder`（`state.go`）：维护运行时状态（idle/streaming/executing）、turn 计数、steering/follow-up 队列、abort 信号。

`loop.go` 中的 `Agent.run` 是主循环：

1. 排空 steering 队列。
2. 必要时压缩上下文（`maybeCompact`）。
3. 流式生成 assistant 消息。
4. 执行工具调用。
5. 发出 turn 事件。
6. 在完成的 turn 边界写入自动 checkpoint。

### 1.3 事件总线

`pkg/events` 提供事件总线，是主要解耦机制。Agent 在运行过程中发出：

- `AgentStart` / `AgentEnd`
- `TurnStart` / `TurnEnd`
- `MessageStart` / `MessageEnd`
- `ToolExecutionStart` / `ToolExecutionEnd`
- `CompactionStarted` / `CompactionCommitted`
- `Error`
- `Audit`

订阅者处理会话持久化、可观测性记录、TUI 更新等。

### 1.4 上下文管理

`pkg/contextmgr.Manager` 把对话组织成多个 block：

- `system`：系统提示。
- `mode`：当前 agent 模式附加的 system prompt。
- `skills`：技能 catalog（每个技能的 `name + description`），模型通过 `use_skill` 工具按需激活完整正文。
- `project_docs`：从 `<repo>/AGENTS.md`、`<repo>/CLAUDE.md`、`<repo>/LCODER.md` 加载的项目上下文（从当前目录向上搜索到 git 根）。
- `recent`：最近的消息。

`BuildTurnRequest` 在 `TokenBudget` 内选择 block、计算 cache 断点、注入临时提醒、解析 `max_tokens`。`MaybeCompactLeveled` 在压力升高时将较旧的 recent 消息折叠为 summary。

### 1.5 UI/Agent 协议边界

UI 层与 agent 层之间有一层正式的协议边界，目标是 **UI 可自由替换**（换框架，乃至将来换语言）：

```
cmd/lcoder          组装层：prepareAgent 产物 → host.NewCore(...)
pkg/tui             唯一的 UI 消费者，只 import pkg/agentapi（+ host.Services）
pkg/host            Core：agentapi.CoreAPI 的实现——session 持久化镜像、
                    会话切换（OpenSession/NewSession/TruncateAfter）、
                    SetMode 换入、goal driver goroutine、checkpoint 操作
pkg/agentapi        纯协议包：CoreAPI 接口 + DTO + 审批类型。
                    只 import 叶子包（models/events/task/checkpoint），
                    禁止 import pkg/agent
pkg/agent           引擎。*Agent 实现 CoreAPI 大部分方法（core.go 适配片段）
```

依赖方向严格单向：`pkg/tui → pkg/agentapi ← pkg/agent ← pkg/host`。`pkg/tui/deps_test.go` 在 CI 中断言 tui 不 import `pkg/agent`/`pkg/session`/`pkg/contextmgr`/`pkg/checkpoint`。

要点：

- **事件是 agent→UI 的唯一状态通道**（`pkg/events`，全部带 json tag，`events.UnmarshalJSON` 可按 type 判别反序列化，`roundtrip_test.go` 保证每种事件可 JSON 往返——这是"将来一定能跨进程"的纪律）。
- **审批是反向请求-响应**（`agentapi.UserConfirmation`），同进程是直接调用，将来跨进程时接口签名不变。
- **会话持久化在 host 侧**（sessionMirror 同步订阅 TurnEnd/AgentEnd），不在 UI；不变量：session 落盘 ≥ checkpoint。
- **goal 驱动在 host 侧**（driver goroutine），UI 只消费 `GoalUpdatedEvent`。
- 新 UI 的最小接入面：实现/持有一个 `agentapi.CoreAPI` 句柄 + 订阅事件总线 + 提供 `UserConfirmation`。headless 模式（`--goal`/`--json`/`-p`）绕过 host 直接用 `*agent.Agent`。
- **跨语言传输已落地**：`pkg/rpcserver` 把 CoreAPI + 事件总线 + 审批桥暴露为 stdio JSONL RPC（`lcoder rpc` 子命令），任何语言的 UI 都能驱动 agent——这是协议边界的第一个跨语言传输，协议细节见 `docs/rpc-protocol.md`。
- 有意不做：provider/mcp/skills 面板仍是 TUI 本地服务（经 `host.Services` 注入）；多 session 路由字段、HTTP/SSE 传输留待后续阶段。

---

## 2. 开发环境搭建

### 2.1 克隆与构建

```bash
git clone https://github.com/lcoder/lcoder.git
cd lcoder
go build ./...
```

### 2.2 运行测试

```bash
# 运行所有单元测试（排除 reference/Shannon，它包含破坏 vet/test 的内部包导入）
go test $(go list ./... | grep -v 'reference/Shannon')

# 运行单个测试
go test ./pkg/agent -run TestAgentCheckpointRoundTrip -v

# Vet
go vet $(go list ./... | grep -v 'reference/Shannon')
```

### 2.3 运行集成测试

集成测试带有 `integration` 构建标签，使用脚本化的 `llmtest` 客户端，不需要真实 API key：

```bash
go test -tags integration ./test/integration -run TestAgentCrashCheckpointResume -v
```

### 2.4 代码约定

- 不要修改 `reference/` 下的代码，它是外部参考材料。
- 本地 `.claude/CLAUDE.md` 是项目约定，开发前应阅读。
- 全局 `~/.claude/CLAUDE.md` 是 Claude Code 的全局原则，也适用于本项目。
- 核心原则：先思考再编码、简单优先、精确修改、目标驱动执行。

---

## 3. 核心包导览

| 包 | 职责 |
|---|---|
| `pkg/agent` | Agent 主循环、流式生成、工具执行、状态管理、checkpoint。 |
| `pkg/contextmgr` | 对话 block 管理、token 预算、压缩、cache 断点。 |
| `pkg/llm` | LLM 客户端门面；子包 `engine`（路由重试）、`catalog`（模型目录）、`provider`（HTTP+SSE 适配器）。 |
| `pkg/tools` | 工具注册表、内置工具、MCP 工具、deferred 加载。 |
| `pkg/events` | 事件总线与事件类型定义。 |
| `pkg/session` | JSONL 会话存储、`parent_id` 分支重建。 |
| `pkg/checkpoint` | 轻量级运行时状态快照。 |
| `pkg/tui` | Bubble Tea 终端 UI。 |
| `pkg/config` | koanf 配置加载与验证。 |
| `pkg/permissions` | 权限引擎与规则匹配。 |
| `pkg/subagent` | 子代理 profile 发现（`.md` frontmatter）与 `Spawner` 边界类型。 |
| `pkg/agenthost` | 同进程子代理宿主：spawn/resume、journal 持久化、预算与保底摘要。 |
| `pkg/observability` | 事件收集、指标、trace、导出器。 |
| `pkg/extension` | 包/扩展管理与进程外扩展运行时（`proto`/`runtime`/`bridge`）。 |

### 3.1 关键接口速览

**工具接口**（`pkg/tools/base.go`）：

```go
type Executable interface {
    Definition() models.ToolDefinition
    Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error)
}
```

**扩展运行时**（`pkg/extension/runtime`）：

进程外扩展以独立进程运行，通过 stdio JSON-RPC 与宿主通信；宿主侧由 `pkg/extension/bridge` 将运行时适配到 agent 钩子、事件与 session。

**Hook 类型**（`pkg/agent/loop.go`）：

```go
type BeforeToolCallHook func(ctx context.Context, info ToolCallInfo) (*BeforeToolCallResult, error)
type AfterToolCallHook func(ctx context.Context, info ToolCallResultInfo) (*AfterToolCallResult, error)
type TransformContext func(ctx context.Context, messages []models.AgentMessage) ([]models.AgentMessage, error)
type ShouldStopFunc func(ctx context.Context, turn TurnSummary) (bool, error)
```

**可观测性导出器接口**（`pkg/observability/observability.go`）：

```go
type Exporter interface {
    Export(record Record) error
    Close() error
}
```

---

## 4. 如何编写自定义 Go 工具扩展

下面以 `examples/extension-tool/main.go` 为例，说明如何编写一个自定义工具。

### 4.1 完整示例

```go
// Package main implements a custom Lcoder tool extension.
// It registers a "weather" tool that returns fake weather data.
package main

import (
    "context"
    "fmt"

    "github.com/lcoder/lcoder/pkg/models"
    "github.com/lcoder/lcoder/pkg/tools"
)

func init() {
    tools.DefaultFactories.Register("weather", newWeatherTool)
}

func main() {
    // Placeholder so `go build` succeeds for this importable extension.
}

type weatherTool struct{}

func newWeatherTool(cwd string) tools.Executable {
    return &weatherTool{}
}

func (w *weatherTool) Definition() models.ToolDefinition {
    return models.ToolDefinition{
        Name:        "weather",
        Description: "Get the current weather for a city",
        Parameters: map[string]any{
            "type": "object",
            "properties": map[string]any{
                "city": map[string]any{
                    "type":        "string",
                    "description": "City name",
                },
            },
            "required": []string{"city"},
        },
    }
}

func (w *weatherTool) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
    city, _ := args["city"].(string)
    if city == "" {
        return models.NewToolExecutionResultError("city is required"), nil
    }
    return models.ToolExecutionResult{
        Content: []models.ContentPart{
            models.TextContent{Text: fmt.Sprintf("The weather in %s is sunny, 24°C.", city)},
        },
    }, nil
}
```

### 4.2 关键步骤

1. **实现 `tools.Executable` 接口**：提供 `Definition()` 和 `Execute(...)`。
2. **定义 JSON Schema 参数**：`Parameters` 字段是 `map[string]any`，按 JSON Schema 格式描述。
3. **注册工厂**：在 `init()` 中调用 `tools.DefaultFactories.Register(name, factory)`。
4. **加载方式**：Go plugin（`.so`）载体已退役。进程外扩展通过扩展运行时接入，设计见 `docs/superpowers/specs/2026-07-24-extension-runtime-design.md`。

### 4.3 安装扩展

```bash
./lcoder install ./examples/extension-tool --name weather --local
```

> `./lcoder install` 仅将扩展源码或包复制到 `~/.lcoder/extensions/` 或 `~/.lcoder/packages/`，不会自动注册。外部工具统一通过 MCP 接入（`http_tools` 与 `tool_extensions` 已退役）；Go 扩展请通过进程外扩展运行时接入（`docs/superpowers/specs/2026-07-24-extension-runtime-design.md`）。

### 4.4 带状态的扩展

如果工具需要保存状态或访问当前工作目录，可以在工厂函数中接收 `cwd`：

```go
func newMyTool(cwd string) tools.Executable {
    return &myTool{cwd: cwd}
}
```

## 5. 如何编写 before-tool-call Hook

before-tool-call hook 在参数验证和权限批准之后、工具实际执行之前运行，可用于审计、敏感文件检查、命令黑名单等。

### 5.1 完整示例

```go
// Package main implements a custom Lcoder hook extension.
// It blocks all write/edit operations to files named README.md.
package main

import (
    "context"

    "github.com/lcoder/lcoder/pkg/agent"
)

// ReadmeProtector returns a BeforeToolCallHook that blocks modifications to README.md.
func ReadmeProtector() agent.BeforeToolCallHook {
    return func(ctx context.Context, info agent.ToolCallInfo) (*agent.BeforeToolCallResult, error) {
        if info.ToolCall.Name != "write" && info.ToolCall.Name != "edit" {
            return nil, nil
        }
        path, _ := info.Args["path"].(string)
        if path == "README.md" {
            return &agent.BeforeToolCallResult{
                Block:  true,
                Reason: "README.md is protected by the readme-protector hook",
            }, nil
        }
        return nil, nil
    }
}

func main() {}
```

### 5.2 Hook 返回值

- 返回 `nil, nil`：不拦截，继续执行工具。
- 返回 `*BeforeToolCallResult{Block: true, Reason: "..."}`：拦截该工具调用，Reason 会返回给模型。
- 返回非 nil error：工具调用失败，错误会返回给模型。

### 5.3 组合多个 Hook

可以使用 `pkg/agent/hooks` 中的 `CompositeBeforeToolCall` 把多个 hook 串起来：

```go
combined := hooks.CompositeBeforeToolCall(
    hooks.ShellBeforeToolCall(cfg.BeforeToolCall, sessionID),
    myCustomHook,
)
```

第一个返回 `Block: true` 的结果会生效，后续 hook 不再执行。

### 5.4 通过配置启用 Hook

Hook 通过 `~/.lcoder/config.yaml` 配置，所有 hook 都是 shell 命令：

```yaml
hooks:
  before_tool_call:
    enabled: true
    command: "python3 ~/.lcoder/hooks/guard.py"
    timeout: 30
  after_tool_result:
    enabled: true
    command: "python3 ~/.lcoder/hooks/log.py"
```

Shell 命令通过 stdin 接收 JSON 上下文，退出码 0=允许、2=拒绝。

## 6. 如何编写自定义 Observability Exporter

Lcoder 的可观测性系统基于事件订阅。自定义 exporter 需要实现 `observability.Exporter` 接口，并注册到 `observability.DefaultRegistry()`。

### 6.1 Exporter 接口

```go
type Exporter interface {
    Export(record Record) error
    Close() error
}
```

`Record` 是联合类型：

```go
type Record struct {
    Type string
    *Span
    *Metric
}
```

- `type` 为 `"span_start"` / `"span_end"` 时，`Span` 字段有效。
- `type` 为 `"metric"` 时，`Metric` 字段有效。

### 6.2 完整示例

```go
// Package main implements a custom Lcoder observability exporter.
// It writes metrics to stdout as JSONL.
package main

import (
    "encoding/json"
    "fmt"
    "os"

    "github.com/lcoder/lcoder/pkg/observability"
)

type StdoutExporter struct{}

func NewStdoutExporter() *StdoutExporter {
    return &StdoutExporter{}
}

func (e *StdoutExporter) Export(record observability.Record) error {
    data, err := json.Marshal(record)
    if err != nil {
        return err
    }
    fmt.Println(string(data))
    return nil
}

func (e *StdoutExporter) Close() error { return nil }

func main() {
    observability.DefaultRegistry().Register("stdout", func(cfg map[string]any, output string) (observability.Exporter, error) {
        return NewStdoutExporter(), nil
    })
    fmt.Fprintln(os.Stderr, "stdout exporter registered")
}
```

### 6.3 注册与使用

在扩展的 `main()` 中注册 factory 后，用户可以在 `~/.lcoder/observability.yaml` 中引用该 exporter：

```yaml
exporter:
  name: stdout
```

> 注意：exporter 的加载和配置方式可能随版本演进，具体请参考 `pkg/observability/setup.go` 和 `configs/runtime/observability.yaml`。

### 6.4 常见 Exporter 模式

- **写入文件**：每条 record 序列化为 JSONL 追加写入。
- **写入数据库**：如 SQLite exporter，批量插入或单条插入。
- **推送远程**：如 Prometheus exporter，在内存中聚合后对外暴露。
- **实时分析**：过滤特定 metric 并触发告警。

---

## 7. 如何添加 Agent Mode

Agent mode 是一组 system prompt 与工具白名单/黑名单配置。无需打包，把一个 YAML 文件放进 mode 搜索目录即可。

### 8.1 Mode 定义文件

新建 `review.yaml`（参考 `configs/prompts/modes/plan.yaml` 或 `examples/extension-mode/review.yaml`）：

```yaml
name: review
description: Focused code review mode
system_prompt: |
  You are in review mode. Analyze the code for correctness, readability,
  performance, and security. Do not make edits; only provide written feedback.
allowed_tools:
  - read
  - grep
  - find
  - ls
denied_tools:
  - write
  - edit
  - bash
```

### 8.2 放置位置与覆盖规则

按优先级从低到高，同名 mode 后者覆盖前者：

1. 内嵌默认 modes（`configs/prompts/modes/*.yaml`，随二进制分发）
2. `~/.lcoder/modes/*.yaml`（用户级）
3. `<项目>/.lcoder/modes/*.yaml`（项目级）

### 8.3 使用

```bash
./lcoder modes                              # 列出全部 mode
./lcoder --mode review -p "review pkg/agent/loop.go"
```

### 8.4 Mode 配置字段

| 字段 | 说明 |
|---|---|
| `name` | 模式名称，命令行 `--mode` 使用。 |
| `description` | 模式描述，`./lcoder modes` 显示。 |
| `system_prompt` | 模式指令全文。作为 ephemeral reminder 注入到消息尾部，不写入 system prompt。 |
| `sparse_prompt` | 精简提醒（可选）。全文注入后，间隔 ≥2 轮发送此文本；距上次全文满 5 轮时重新发全文；其余轮次完全静默。应只重申硬约束并指回前文，不重复全文。留空则稀疏档同样静默（不再回退全文），直到 5 轮全文刷新。 |
| `allowed_tools` | 允许使用的工具列表；设置后列表外的工具在执行时被拒绝。 |
| `denied_tools` | 禁止使用的工具列表；命中的工具在执行时被拒绝。 |

模式的工具限制是**执行时拒绝**，而非过滤工具 schema：工具数组是 provider 缓存前缀的第一层，切换模式时改动它会导致整段对话按全新输入重新计费。模型仍会看到被限制工具的完整 schema，调用后拿到一条带脱困路径的 `tool_result` 错误。同理，模式指令走 ephemeral reminder 落在最后一个缓存断点之后，因此只花它自身的字节，不动缓存前缀。

---

## 8. 如何接入 MCP 服务器

MCP 服务器是外部工具服务，Lcoder 作为客户端连接。本节面向**配置和调试**，不涉及 MCP 服务端的开发。

### 9.1 配置 stdio MCP 服务器

```yaml
mcp_servers:
  - name: filesystem
    transport: stdio
    command: ["npx", "-y", "@modelcontextprotocol/server-filesystem", "."]
    env:
      NODE_ENV: production
```

### 9.2 配置 SSE MCP 服务器

```yaml
mcp_servers:
  - name: remote-sse
    transport: sse
    url: http://localhost:3000
    headers:
      Authorization: Bearer ${REMOTE_MCP_TOKEN}
    timeout: 60
```

### 9.3 配置 Streamable HTTP MCP 服务器

```yaml
mcp_servers:
  - name: remote-http
    transport: streamable-http
    url: https://mcp.example.com/v1
    headers:
      Authorization: Bearer ${REMOTE_MCP_TOKEN}
    timeout: 60
```

### 9.4 调试 MCP

1. 检查命令是否已安装：`npx @modelcontextprotocol/server-filesystem --help`。
2. 检查 URL 和端口是否可达。
3. 查看 Lcoder 启动日志中的连接错误。
4. 在 TUI 中输入 `/mcp` 查看连接状态和工具列表。

### 9.5 MCP 工具命名

MCP 工具在 agent 工具列表中显示为 `{serverName}_{toolName}`。例如 `filesystem_read_file`。

---

## 9. 子代理扩展

Lcoder 内置了子代理机制，只需在配置中启用即可使用：

```yaml
subagent:
  enabled: true
```

启用后，agent 会注册一个 `subagent` 工具，可把任务委派给其他 Lcoder agent。需要深度定制 runner 的场景可通过进程外扩展运行时实现自定义子代理扩展（`docs/superpowers/specs/2026-07-24-extension-runtime-design.md`），日常使用无需安装额外扩展。

### 10.1 核心思想

子代理扩展注册一个 `subagent` 工具，工具参数支持三种调用方式：

- **Single**：单个 agent 执行单个任务。
- **Parallel**：多个 agent 并行执行多个任务。
- **Chain**：多个 agent 串行执行，后一步可以引用前一步的结果。

### 10.2 自定义扩展入口

内置子代理已覆盖常见场景。需要自定义 runner 时，可通过进程外扩展运行时实现：扩展是一个带 `extension.yaml` 清单的独立进程，通过 stdio JSON-RPC 与宿主通信（`pkg/extension/runtime`），设计见 `docs/superpowers/specs/2026-07-24-extension-runtime-design.md`。

### 10.3 工具定义

`subagent` 工具的参数使用 `oneOf` 描述三种互斥模式：

```go
Parameters: map[string]any{
    "type": "object",
    "oneOf": []map[string]any{
        {
            "title": "Single",
            "properties": map[string]any{
                "agent": {"type": "string"},
                "task":  {"type": "string"},
                "cwd":   {"type": "string"},
            },
            "required": []string{"agent", "task"},
        },
        // Parallel, Chain ...
    },
},
```

### 10.4 使用

在对话中：

```text
请使用 subagent 工具，让 worker agent 帮我分析 pkg/llm 目录的代码结构
```

## 10. 测试与调试

### 11.1 单元测试

Lcoder 使用标准 `go test`。由于 `reference/Shannon` 包含破坏模块 vet/test 的内部包导入，测试时需要排除它：

```bash
go test $(go list ./... | grep -v 'reference/Shannon')
```

### 11.2 集成测试

集成测试位于 `test/integration/`，带有 `integration` 构建标签，使用脚本化 `llmtest` 客户端：

```bash
go test -tags integration ./test/integration -run TestAgentCrashCheckpointResume -v
```

### 11.3 调试单个组件

- **Agent 行为**：在 `pkg/agent/loop.go` 的 `run` 方法中添加日志。
- **上下文管理**：使用 `pkg/contextmgr` 的测试用例验证 block 选择和压缩。
- **工具执行**：使用 `pkg/tools` 的测试用例。
- **LLM 调用**：使用 `pkg/llm/llmtest` 模拟客户端。

### 11.4 使用 `lcoder --json` 调试

JSON 模式输出每一事件，便于观察 agent 内部状态：

```bash
./lcoder --json -p "分析 main.go" 2>/dev/null | jq .
```

### 11.5 CI 流程

`.github/workflows/ci.yml` 运行：

```bash
go build ./...
go vet ./...
go test ./... -count=1 -race
```

本地测试建议也使用相同命令，但排除 `reference/Shannon`。

---

## 11. 贡献指南与代码约定

### 12.1 提交前检查

```bash
go build ./...
go vet $(go list ./... | grep -v 'reference/Shannon')
go test $(go list ./... | grep -v 'reference/Shannon') -count=1 -race
```

### 12.2 代码风格

- 匹配周围代码的注释密度、命名和习惯用法。
- 不添加超出需求的功能。
- 不为仅使用一次的代码创建抽象。
- 不修改无关的相邻代码、注释或格式。
- 当修改造成孤立代码时，删除不再使用的导入、变量、函数。

### 12.3 文档

- 新增配置字段需在 `configs/runtime/lcoder.yaml` 中添加示例和注释。
- 新增 CLI 命令需在 `README.md` / `README_EN.md` 和本指南中同步更新。
- 复杂逻辑需添加注释说明设计意图。

### 12.4 测试要求

- Bug 修复：先写复现测试，再修复。
- 新功能：需包含单元测试或集成测试。
- 重构：确保重构前后测试全部通过。

### 12.5 提交信息

建议采用简洁的提交信息格式：

```
feat(contextmgr): add proactive compaction
fix(agent): resolve deadlock in tool execution
docs(user-guide): add MCP server examples
```

---

## 12. 扩展 API 速查

### 13.1 `tools.Executable`

```go
type Executable interface {
    Definition() models.ToolDefinition
    Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error)
}
```

### 13.2 `tools.Factory`

```go
type Factory func(cwd string) Executable
```

### 13.3 进程外扩展运行时

进程外扩展是独立进程，通过 stdio JSON-RPC 与宿主通信：

- `pkg/extension/proto`：JSON-RPC 线路类型。
- `pkg/extension/runtime`：清单发现、进程生命周期、宿主侧握手/钩子/事件/命令。
- `pkg/extension/bridge`：将运行时适配到 agent 钩子、summarizer、事件与 session。

设计文档见 `docs/superpowers/specs/2026-07-24-extension-runtime-design.md`。

### 13.4 `agent.BeforeToolCallHook`

```go
type BeforeToolCallHook func(ctx context.Context, info ToolCallInfo) (*BeforeToolCallResult, error)

type ToolCallInfo struct {
    AssistantMessage models.AgentMessage
    ToolCall         models.ToolCallContent
    Args             map[string]any
    Context          []models.AgentMessage
}

type BeforeToolCallResult struct {
    Block  bool
    Reason string
}
```

### 13.5 `observability.Exporter`

```go
type Exporter interface {
    Export(record Record) error
    Close() error
}
```

### 13.6 `observability.ExporterFactory`

```go
type ExporterFactory func(cfg map[string]any, output string) (Exporter, error)
```

### 13.7 Mode 定义文件

```yaml
name: review
description: Focused code review mode
system_prompt: |
  You are in review mode...
allowed_tools:
  - read
  - grep
denied_tools:
  - write
  - edit
model: gpt-4o-mini
provider: openai
execution_mode: parallel
```

### 13.8 Agent Builder 链式调用

```go
ag, err := agent.NewBuilder().
    WithConfig(agent.Config{...}).
    WithGatewayClient(llmClient).
    WithRegistry(registry).
    WithPermissions(permEngine).
    WithEventBus(bus).
    WithObservability(obsCollector).
    WithContextManager(mgr).
    WithBeforeToolCall(myHook).
    Build()
```

### 13.9 事件类型

常用事件：

- `events.AgentStartEvent`
- `events.AgentEndEvent`
- `events.TurnStartEvent`
- `events.TurnEndEvent`
- `events.MessageStartEvent`
- `events.MessageEndEvent`
- `events.ToolExecutionStartEvent`
- `events.ToolExecutionEndEvent`
- `events.CompactionStartedEvent`
- `events.CompactionCommittedEvent`
- `events.ErrorEvent`
- `events.AuditEvent`

订阅示例：

```go
unsub := bus.Subscribe(func(ctx context.Context, ev events.Event) error {
    switch e := ev.(type) {
    case events.ToolExecutionEndEvent:
        fmt.Printf("tool %s finished in %d ms\n", e.ToolName, e.DurationMs)
    }
    return nil
})
defer unsub()
```

---

> 本指南基于 Lcoder 当前实现编写。扩展 API 可能随版本变化，开发时请以源码为准。
