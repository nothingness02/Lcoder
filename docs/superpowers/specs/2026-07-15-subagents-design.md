# Subagent Extension Design

> **Goal:** 参考 pi 的 subagent 扩展，通过 Lcoder 现有的 extension/工具扩展系统，为 code agent 增加 `subagent` 工具，使其能够调用其他专用子 agent（独立 lcoder 子进程）完成 single / parallel / chain 任务。

## 背景

Lcoder 已具备 extension 系统：Go plugin 可通过 `extension.Extension` 接口注册工具和 hooks，并在 `tool_extensions` 中配置加载。pi 的 subagent 扩展通过启动独立 pi 子进程实现上下文隔离。本设计采用相同思路：在 Lcoder 中启动独立 `lcoder` 子进程作为 subagent。

## 设计决策

- **执行模型**：子进程（调用 `lcoder --json -p ...`）。
- **接入形态**：Go 插件扩展，示例位于 `examples/extension-subagent/`。
- **Agent 定义**：Markdown + YAML frontmatter，放在 `~/.lcoder/agents/*.md` 和项目级 `.lcoder/agents/*.md`。
- **执行模式**：single、parallel、chain。
- **信任机制**：第一版不添加确认，文件存在即加载。
- **返回格式**：仅文本（子 agent 最终回复）。
- **max_turns**：第一版不限制，由子 agent 自行决定结束时机；任务应被拆分为小而可控的单元。
- **cwd**：用户传入的 `cwd` 必须位于项目根目录下，否则返回错误。
- **parallel 失败策略**：单个任务失败不影响其他任务；最终结果列表中对应位置返回错误原因字符串。

## 总体架构

```
pkg/subagent/              // 可复用核心库
  agents.go                // 扫描、解析 agent 定义
  runner.go                // single/parallel/chain 调度
  invoke.go                // 构建并运行 lcoder 子进程
  result.go                // 从 JSONL 事件流提取最终文本
  errors.go                // 错误类型

examples/extension-subagent/
  main.go                  // Go plugin 入口，实现 extension.Extension
  lcoder-extension.yaml    // 扩展元数据
  README.md                // 构建与使用说明
```

父 agent 调用 `subagent` 工具时，插件创建 `pkg/subagent.Runner` 并转发调用。Runner 负责启动子进程、解析事件、返回结果文本。

## Agent 定义

### 扫描路径

- 用户级：`~/.lcoder/agents/*.md`
- 项目级：`{projectRoot}/.lcoder/agents/*.md`

项目级 agent 只有在位于项目根目录下时才被加载，不向上递归。

### 文件格式

```markdown
---
name: worker
description: 通用实现子代理
model: gpt-4o-mini
provider: openai
mode: code
timeout: 120
---
你是一位专注于实现的助手。请直接给出可运行的代码和改动。
```

### 字段

| 字段 | 必需 | 说明 |
|---|---|---|
| `name` | 是 | 调用时使用的 agent 名称，唯一标识 |
| `description` | 否 | 出现在工具 schema 的枚举描述中 |
| `model` | 否 | 覆盖父 agent 的模型 ID |
| `provider` | 否 | 覆盖父 agent 的 provider |
| `mode` | 否 | agent 模式，默认 `code` |
| `timeout` | 否 | 子进程超时秒数，默认 120 |

## 工具 schema

工具名：`subagent`

参数使用 `oneOf` 区分三种模式：

### single

```json
{
  "agent": "worker",
  "task": "实现一个 HTTP 客户端",
  "cwd": "./pkg/foo"
}
```

### parallel

```json
{
  "tasks": [
    {"agent": "worker", "task": "实现 func A", "cwd": "./pkg/a"},
    {"agent": "worker", "task": "实现 func B", "cwd": "./pkg/b"}
  ]
}
```

约束：最多 8 个任务，并发度 4。

### chain

```json
{
  "chain": [
    {"agent": "scout", "task": "列出需要改动的文件"},
    {"agent": "worker", "task": "基于前一步结果：{previous}"}
  ]
}
```

`{previous}` 会被替换为上一个 agent 的返回文本。

## 核心包接口

```go
package subagent

// Agent is a loaded agent definition.
type Agent struct {
    Name        string
    Description string
    Model       string
    Provider    string
    Mode        string
    Timeout     int
    Prompt      string // markdown body after frontmatter
}

// TaskItem is a single parallel task.
type TaskItem struct {
    Agent string
    Task  string
    CWD   string
}

// ChainItem is a single chain step.
type ChainItem struct {
    Agent string
    Task  string
    CWD   string
}

// Runner executes subagent invocations.
type Runner interface {
    RunSingle(ctx context.Context, agent Agent, task string, cwd string) (string, error)
    RunParallel(ctx context.Context, projectRoot string, items []TaskItem) ([]Result, error)
    RunChain(ctx context.Context, projectRoot string, items []ChainItem) (string, error)
}

// Result is the outcome of one parallel task.
type Result struct {
    Text string
    Err  error
}
```

### 默认 Runner

```go
func NewRunner(projectRoot string) Runner
```

Runner 持有项目根目录，用于校验 `cwd`。

## 子进程调用

### 命令行

```bash
lcoder --json -p "<task>" --model <model> --provider <provider> --mode <mode>
```

- `-p` / `--prompt` 传递任务文本。
- `--json` 使子进程以 JSONL 事件流输出到 stdout。
- 其他参数根据 agent 定义和父配置拼接。

### 上下文与超时

- 使用 `exec.CommandContext`，父 context 取消时自动 kill 子进程。
- 每个子进程使用 agent 定义中的 `timeout`，通过 `context.WithTimeout` 包装。

### 事件解析

子进程 stdout 每行是一个 JSON 对象。`pkg/subagent/result.go` 使用 discriminator 解析 `events.Event`：

1. 先 unmarshal 出 `type` 字段。
2. 根据 `type` unmarshal 为具体事件结构（`AgentEndEvent`、`MessageEndEvent`、`ErrorEvent` 等）。
3. 从 `AgentEndEvent.Messages` 中取最后一条 `role == assistant` 的消息，调用 `Text()` 作为结果。

`events` 包目前没有现成的 unmarshal 函数，因此 `pkg/subagent` 需要自己维护一个 `type -> struct` 映射。

### 会话副作用

当前 CLI 没有 `--no-session` 或 `--ephemeral` 标志，子进程会创建 session 和 checkpoint 文件。第一版接受这一点，在 README 中说明；后续可考虑新增 `--ephemeral` 标志，或设置临时 `HOME` 来隔离。

## 插件层

`examples/extension-subagent/main.go` 导出符号：

```go
func New(cfg map[string]any) (extension.Extension, error)
```

实现 `extension.Extension`：

```go
type subagentExtension struct {
    projectRoot string
    runner      subagent.Runner
}

func (e *subagentExtension) Name() string { return "subagent" }

func (e *subagentExtension) RegisterTools(registry *tools.Registry, cwd string) error {
    e.projectRoot = cwd
    e.runner = subagent.NewRunner(cwd)
    registry.Register("subagent", tools.NewFuncExecutable(...))
    return nil
}

func (e *subagentExtension) RegisterHooks() (extension.Hooks, error) {
    return extension.Hooks{}, nil
}

func (e *subagentExtension) RegisterExporters() (map[string]observability.ExporterFactory, error) {
    return nil, nil
}
```

工具执行函数负责：
1. 根据参数解析出 single / parallel / chain。
2. 发现 agent 定义。
3. 校验 `cwd` 位于 `projectRoot` 下。
4. 调用 Runner 对应方法。
5. 将结果文本（或 parallel 结果列表的拼接文本）返回。

## 配置示例

用户 `config.yaml`：

```yaml
tool_extensions:
  - name: subagent
    type: go-plugin
    path: /path/to/subagent.so
    config: {}
```

构建插件：

```bash
cd examples/extension-subagent
go build -buildmode=plugin -o subagent.so .
```

## 错误处理

- 未知 agent：返回 `subagent: unknown agent "name"`。
- `cwd` 越界：返回 `subagent: cwd "..." is outside project root "..."`。
- 子进程非零退出 / stdout 无法解析：返回包含 stderr 的错误。
- `context.Canceled`：kill 子进程，向上返回 context 错误。
- parallel 单个任务失败：该任务结果位置返回错误文本，其他任务继续运行。

## 测试

- `pkg/subagent/agents_test.go`：
  - 测试 frontmatter 解析。
  - 测试 discoverAgents 扫描用户级和项目级目录。
- `pkg/subagent/runner_test.go`：
  - 使用 fake `Runner` 实现或 fake executor 测试 single/parallel/chain 调度。
  - 测试 parallel 失败继续策略。
  - 测试 `cwd` 越界校验。
- `pkg/subagent/result_test.go`：
  - 测试 JSONL 事件解析。
  - 测试最终答案提取（AgentEndEvent / MessageEndEvent）。
- `examples/extension-subagent`：
  - README 说明构建和配置步骤。
  - 可选：集成测试验证插件可以被 `extension.PluginLoader` 加载。

## 后续可扩展

- 增加 `--ephemeral` CLI flag，避免子进程遗留 session。
- 支持 agent 定义中声明允许/禁止的工具列表。
- 支持返回结构化 JSON（usage、cost、turns）。
- 支持 project-local agent 的信任确认。
