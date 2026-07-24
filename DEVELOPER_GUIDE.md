# Lcoder 开发者快速上手指南

> 本文档面向希望扩展、调试或贡献 Lcoder 的开发者。如果你是普通用户，请阅读 `USER_GUIDE.md`。

## 目录

1. [架构概览](#1-架构概览)
2. [开发环境搭建](#2-开发环境搭建)
3. [核心包导览](#3-核心包导览)
4. [如何编写自定义 Go 工具扩展](#4-如何编写自定义-go-工具扩展)
5. [如何编写 before-tool-call Hook](#5-如何编写-before-tool-call-hook)
6. [如何编写自定义 Observability Exporter](#6-如何编写自定义-observability-exporter)
7. [如何添加 HTTP 工具](#7-如何添加-http-工具)
8. [如何添加 Agent Mode](#8-如何添加-agent-mode)
9. [如何接入 MCP 服务器](#9-如何接入-mcp-服务器)
10. [子代理扩展](#10-子代理扩展)
11. [测试与调试](#11-测试与调试)
12. [贡献指南与代码约定](#12-贡献指南与代码约定)
13. [扩展 API 速查](#13-扩展-api-速查)

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
| `pkg/tools` | 工具注册表、内置工具、HTTP 工具、MCP 工具、deferred 加载。 |
| `pkg/events` | 事件总线与事件类型定义。 |
| `pkg/session` | JSONL 会话存储、`parent_id` 分支重建。 |
| `pkg/checkpoint` | 轻量级运行时状态快照。 |
| `pkg/tui` | Bubble Tea 终端 UI。 |
| `pkg/config` | koanf 配置加载与验证。 |
| `pkg/permissions` | 权限引擎与规则匹配。 |
| `pkg/observability` | 事件收集、指标、trace、导出器。 |
| `pkg/extension` | 进程内扩展宿主接口、包/扩展管理与进程外扩展运行时（`proto`/`runtime`/`bridge`）。 |
| `pkg/memory` | 持久化记忆与动态召回。 |
| `pkg/codeindex` | SQLite 代码图索引。 |

### 3.1 关键接口速览

**工具接口**（`pkg/tools/base.go`）：

```go
type Executable interface {
    Definition() models.ToolDefinition
    Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error)
}
```

**扩展接口**（`pkg/extension/extension.go`）：

```go
type Extension interface {
    Name() string
    RegisterTools(registry *tools.Registry, cwd string) error
    RegisterHooks() (Hooks, error)
    RegisterExporters() (map[string]observability.ExporterFactory, error)
}
```

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

> `./lcoder install` 仅将扩展源码或包复制到 `~/.lcoder/extensions/` 或 `~/.lcoder/packages/`，不会自动注册。`tool_extensions` 目前支持 `type: json`（HTTP 工具描述符）；Go 扩展请通过进程外扩展运行时接入（`docs/superpowers/specs/2026-07-24-extension-runtime-design.md`）。

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
    hooks.SensitiveFileCheck(...),
    hooks.BashDenylist(...),
    myCustomHook,
)
```

第一个返回 `Block: true` 的结果会生效，后续 hook 不再执行。

### 5.4 通过配置启用 Hook

部分 hook 可以直接在 `~/.lcoder/config.yaml` 中配置：

```yaml
hooks:
  audit:
    enabled: true
  sensitive_file_check:
    enabled: true
    patterns: ["*.env", "*.key", "*.pem"]
  bash_denylist:
    enabled: true
    patterns: ["rm -rf /", "mkfs.*"]
```

`cmd/lcoder/main.go` 中的 `makeBeforeToolCall` 会把这些配置转换成 hook 链。

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

> 注意：exporter 的加载和配置方式可能随版本演进，具体请参考 `pkg/observability/setup.go` 和 `configs/observability.yaml`。

### 6.4 常见 Exporter 模式

- **写入文件**：每条 record 序列化为 JSONL 追加写入。
- **写入数据库**：如 SQLite exporter，批量插入或单条插入。
- **推送远程**：如 Prometheus exporter，在内存中聚合后对外暴露。
- **实时分析**：过滤特定 metric 并触发告警。

---

## 7. 如何添加 HTTP 工具

HTTP 工具不需要写 Go 代码，直接在 `~/.lcoder/config.yaml` 中声明即可。

### 7.1 最小示例

```yaml
http_tools:
  - name: deploy
    endpoint: http://localhost:9001/deploy
    description: Deploy service to staging
    parameters:
      type: object
      properties:
        service:
          type: string
      required: [service]
    execution_mode: parallel
    headers:
      Authorization: Bearer ${DEPLOY_TOKEN}
```

### 7.2 字段说明

| 字段 | 说明 |
|---|---|
| `name` | 工具名称，在 agent 工具列表中显示。 |
| `endpoint` | 接收 POST 请求的端点。 |
| `description` | 工具描述，帮助 LLM 决定何时调用。 |
| `parameters` | 符合 JSON Schema 的参数定义。 |
| `execution_mode` | `parallel` 或 `serial`。 |
| `headers` | 自定义请求头，支持 `${VAR}` 环境变量。 |

### 7.3 实现 HTTP 工具端点

你的服务端需要接收 POST 请求，请求体为 JSON：

```json
{
  "tool_call_id": "call_xxx",
  "name": "deploy",
  "arguments": {"service": "api"},
  "context": {"cwd": "/path/to/project"}
}
```

响应体应为 tool 结果格式。最简单的方式是返回一段文本：

```json
{
  "content": [{"type": "text", "text": "Deployment started"}]
}
```

---

## 8. 如何添加 Agent Mode

Agent mode 是一组 system prompt 与工具白名单/黑名单配置。你可以把它打包成一个 mode package。

### 8.1 目录结构

```
my-mode/
  lcoder-package.yaml
  agents/
    review.yaml
```

### 8.2 `lcoder-package.yaml`

```yaml
name: my-mode
version: 1.0.0
author: your-name
description: My custom agent mode
```

### 8.3 Mode 定义文件

`agents/review.yaml`：

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

### 8.4 安装与使用

```bash
./lcoder install --local ./my-mode
```

> `./lcoder install` 仅复制文件，不会自动加载包。安装后需在 `~/.lcoder/config.yaml` 的 `packages` 中声明：

```yaml
packages:
  - name: my-mode
    path: ~/.lcoder/packages/my-mode
```

然后即可使用：

```bash
./lcoder --mode review -p "review pkg/agent/loop.go"
```

### 8.5 Mode 配置字段

| 字段 | 说明 |
|---|---|
| `name` | 模式名称，命令行 `--mode` 使用。 |
| `description` | 模式描述，`./lcoder modes` 显示。 |
| `system_prompt` | 注入到 system prompt 的指令。 |
| `allowed_tools` | 允许使用的工具列表；设置后只有列表内工具可用。 |
| `denied_tools` | 禁止使用的工具列表。 |
| `model` | 该模式使用的模型 ID（可选）。 |
| `provider` | 该模式使用的 provider（可选）。 |
| `execution_mode` | `parallel` 或 `sequential`（可选）。 |

---

## 9. 如何接入 MCP 服务器

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

## 10. 子代理扩展

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

### 10.2 扩展入口

```go
func New(cfg map[string]any) (extension.Extension, error) {
    return &subagentExtension{
        newRunner: subagent.NewRunner,
    }, nil
}
```

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

## 11. 测试与调试

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

## 12. 贡献指南与代码约定

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

- 新增配置字段需在 `configs/lcoder.yaml` 中添加示例和注释。
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

## 13. 扩展 API 速查

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

### 13.3 `extension.Extension`

```go
type Extension interface {
    Name() string
    RegisterTools(registry *tools.Registry, cwd string) error
    RegisterHooks() (Hooks, error)
    RegisterExporters() (map[string]observability.ExporterFactory, error)
}
```

> 这是进程内宿主接口。Go plugin（`.so`）加载载体已退役；进程外扩展请使用扩展运行时（`pkg/extension/proto`/`runtime`/`bridge`），设计文档见 `docs/superpowers/specs/2026-07-24-extension-runtime-design.md`。

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
