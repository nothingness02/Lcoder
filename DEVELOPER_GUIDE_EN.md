# Lcoder Developer Quick Start Guide

> This document is for developers who want to extend, debug, or contribute to Lcoder. If you are an end user, please read `USER_GUIDE_EN.md`.

## Table of Contents

1. [Architecture Overview](#1-architecture-overview)
2. [Development Environment Setup](#2-development-environment-setup)
3. [Core Package Tour](#3-core-package-tour)
4. [How to Write a Custom Go Tool Extension](#4-how-to-write-a-custom-go-tool-extension)
5. [How to Write a before-tool-call Hook](#5-how-to-write-a-before-tool-call-hook)
6. [How to Write a Custom Observability Exporter](#6-how-to-write-a-custom-observability-exporter)
7. [How to Add an HTTP Tool](#7-how-to-add-an-http-tool)
8. [How to Add an Agent Mode](#8-how-to-add-an-agent-mode)
9. [How to Connect an MCP Server](#9-how-to-connect-an-mcp-server)
10. [Subagent Extension](#10-subagent-extension)
11. [Testing and Debugging](#11-testing-and-debugging)
12. [Contribution Guidelines and Code Conventions](#12-contribution-guidelines-and-code-conventions)
13. [Extension API Quick Reference](#13-extension-api-quick-reference)

---

## 1. Architecture Overview

The entry point is `cmd/lcoder/main.go`. The startup flow has two main paths:

1. **Wiring** (`prepareAgent`): assembles configuration, LLM client, tool registry, MCP registry, session store, observability, mode manager, context manager, and finally an `agent.Agent`.
2. **Runtime dispatch** (`runRoot`): chooses one-shot, JSON event stream, or TUI mode based on user input, and writes a crash checkpoint on SIGINT/SIGTERM.

### 1.1 Core Component Relationships

```
cmd/lcoder/main.go
 └─ prepareAgent
     ├─ config.Load()              load ~/.lcoder/config.yaml
     ├─ llm.NewClient(engine)      create LLM client
     ├─ tools.NewRegistry(...)     create tool registry
     ├─ registry.RegisterBuiltinFactories  register built-in tools
     ├─ registry.Register + LoadExtensions  register HTTP extension tools
     ├─ mcp.NewRegistry(...)       connect MCP servers and register their tools
     ├─ session.NewStore(...)      create / load session
     ├─ observability.NewCollectorWithAudit  create observability collector
     ├─ agent.NewModeManager(...)  load built-in and custom modes
     ├─ agentsetup.NewContextManager  create context manager
     └─ agent.NewBuilder().Build() build Agent
```

### 1.2 Agent Internals

The `pkg/agent` package splits the core work across three internal components:

- `streamer` (`streamer.go`): constructs each turn request via `contextmgr.Manager.BuildTurnRequest`, streams LLM events, and assembles assistant messages.
- `executor` (`executor.go`): validates arguments, checks permissions, executes tool calls, and owns deferred tool promotion.
- `stateHolder` (`state.go`): maintains runtime state (idle/streaming/executing), turn counter, steering/follow-up queues, and abort signals.

`Agent.run` in `loop.go` is the main loop:

1. Drain the steering queue.
2. Compact context if needed (`maybeCompact`).
3. Stream an assistant message.
4. Execute tool calls.
5. Emit turn events.
6. Write an automatic checkpoint at the completed-turn boundary.

### 1.3 Event Bus

`pkg/events` provides the event bus, the primary decoupling mechanism. The agent emits:

- `AgentStart` / `AgentEnd`
- `TurnStart` / `TurnEnd`
- `MessageStart` / `MessageEnd`
- `ToolExecutionStart` / `ToolExecutionEnd`
- `CompactionStarted` / `CompactionCommitted`
- `Error`
- `Audit`

Subscribers handle session persistence, observability recording, and TUI updates.

### 1.4 Context Management

`pkg/contextmgr.Manager` organizes the conversation into blocks:

- `system`: system prompt.
- `mode`: additional system prompt from the active agent mode.
- `skills`: the skill catalog (each skill's `name + description`); the model activates a skill's full body on demand via the `use_skill` tool.
- `project_docs`: project context loaded from `<repo>/AGENTS.md`, `<repo>/CLAUDE.md`, and `<repo>/LCODER.md`, searched upward to the git root.
- `recent`: recent messages.

`BuildTurnRequest` selects blocks within `TokenBudget`, computes cache breakpoints, injects ephemeral reminders, and resolves `max_tokens`. `MaybeCompactLeveled` folds older recent messages into a summary when pressure rises.

---

## 2. Development Environment Setup

### 2.1 Clone and Build

```bash
git clone https://github.com/lcoder/lcoder.git
cd lcoder
go build ./...
```

### 2.2 Run Tests

```bash
# Run all unit tests (exclude reference/Shannon, which contains internal-package imports that break vet/test)
go test $(go list ./... | grep -v 'reference/Shannon')

# Run a single test
go test ./pkg/agent -run TestAgentCheckpointRoundTrip -v

# Vet
go vet $(go list ./... | grep -v 'reference/Shannon')
```

### 2.3 Run Integration Tests

Integration tests live under `test/integration/`, are gated by the `integration` build tag, and use the scripted `llmtest` client (no real API key required):

```bash
go test -tags integration ./test/integration -run TestAgentCrashCheckpointResume -v
```

### 2.4 Code Conventions

- Do not modify code under `reference/`; it is external reference material.
- Read the local `.claude/CLAUDE.md` for project conventions before coding.
- The global `~/.claude/CLAUDE.md` contains Claude Code principles that also apply here.
- Core principles: think before coding, prefer simplicity, make precise changes, and be goal-driven.

---

## 3. Core Package Tour

| Package | Responsibility |
|---|---|
| `pkg/agent` | Agent main loop, streaming, tool execution, state management, checkpoints. |
| `pkg/contextmgr` | Conversation block management, token budget, compaction, cache breakpoints. |
| `pkg/llm` | LLM client facade; subpackages `engine` (routing/retry), `catalog` (model catalog), `provider` (HTTP+SSE adapters). |
| `pkg/tools` | Tool registry, built-in tools, HTTP tools, MCP tools, deferred loading. |
| `pkg/events` | Event bus and event type definitions. |
| `pkg/session` | JSONL session storage and `parent_id` branch reconstruction. |
| `pkg/checkpoint` | Lightweight runtime state snapshots. |
| `pkg/tui` | Bubble Tea terminal UI. |
| `pkg/config` | koanf configuration loading and validation. |
| `pkg/permissions` | Permission engine and rule matching. |
| `pkg/observability` | Event collection, metrics, traces, exporters. |
| `pkg/extension` | In-process extension host interface, package/extension management, and the process-external extension runtime (`proto`/`runtime`/`bridge`). |
| `pkg/memory` | Persistent memory and dynamic recall. |
| `pkg/codeindex` | SQLite code graph index. |

### 3.1 Key Interfaces

**Tool interface** (`pkg/tools/base.go`):

```go
type Executable interface {
    Definition() models.ToolDefinition
    Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error)
}
```

**Extension interface** (`pkg/extension/extension.go`):

```go
type Extension interface {
    Name() string
    RegisterTools(registry *tools.Registry, cwd string) error
    RegisterHooks() (Hooks, error)
    RegisterExporters() (map[string]observability.ExporterFactory, error)
}
```

**Hook types** (`pkg/agent/loop.go`):

```go
type BeforeToolCallHook func(ctx context.Context, info ToolCallInfo) (*BeforeToolCallResult, error)
type AfterToolCallHook func(ctx context.Context, info ToolCallResultInfo) (*AfterToolCallResult, error)
type TransformContext func(ctx context.Context, messages []models.AgentMessage) ([]models.AgentMessage, error)
type ShouldStopFunc func(ctx context.Context, turn TurnSummary) (bool, error)
```

**Observability exporter interface** (`pkg/observability/observability.go`):

```go
type Exporter interface {
    Export(record Record) error
    Close() error
}
```

---

## 4. How to Write a Custom Go Tool Extension

This section walks through `examples/extension-tool/main.go`, a custom weather tool.

### 4.1 Full Example

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

### 4.2 Key Steps

1. **Implement `tools.Executable`**: provide `Definition()` and `Execute(...)`.
2. **Define JSON Schema parameters**: the `Parameters` field is a `map[string]any` in JSON Schema format.
3. **Register the factory**: in `init()`, call `tools.DefaultFactories.Register(name, factory)`.
4. **Loading**: the Go plugin (`.so`) carrier is retired. Process-external extensions are integrated via the extension runtime; see `docs/superpowers/specs/2026-07-24-extension-runtime-design.md`.

### 4.3 Install the Extension

```bash
./lcoder install ./examples/extension-tool --name weather --local
```

`./lcoder install` only copies the extension files into `~/.lcoder/extensions/`; it does not register the tool automatically. `tool_extensions` currently supports `type: json` (HTTP tool descriptors); Go extensions should integrate through the process-external extension runtime (`docs/superpowers/specs/2026-07-24-extension-runtime-design.md`).

### 4.4 Stateful Extensions

If the tool needs state or access to the working directory, accept `cwd` in the factory:

```go
func newMyTool(cwd string) tools.Executable {
    return &myTool{cwd: cwd}
}
```

## 5. How to Write a before-tool-call Hook

A before-tool-call hook runs after argument validation and permission approval but before the tool actually executes. It is useful for auditing, sensitive-file checks, command denylists, etc.

### 5.1 Full Example

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

### 5.2 Hook Return Values

- Return `nil, nil`: do not intercept; continue executing the tool.
- Return `*BeforeToolCallResult{Block: true, Reason: "..."}`: block the tool call; the reason is returned to the model.
- Return a non-nil error: the tool call fails and the error is returned to the model.

### 5.3 Combining Multiple Hooks

Use `pkg/agent/hooks.CompositeBeforeToolCall` to chain hooks:

```go
combined := hooks.CompositeBeforeToolCall(
    hooks.SensitiveFileCheck(...),
    hooks.BashDenylist(...),
    myCustomHook,
)
```

The first result with `Block: true` wins; remaining hooks are skipped.

### 5.4 Enabling Hooks via Configuration

Some hooks can be enabled directly in `~/.lcoder/config.yaml`:

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

`makeBeforeToolCall` in `cmd/lcoder/main.go` turns this configuration into a hook chain.

## 6. How to Write a Custom Observability Exporter

Lcoder's observability system is event-driven. A custom exporter implements `observability.Exporter` and registers itself with `observability.DefaultRegistry()`.

### 6.1 Exporter Interface

```go
type Exporter interface {
    Export(record Record) error
    Close() error
}
```

`Record` is a union type:

```go
type Record struct {
    Type string
    *Span
    *Metric
}
```

- When `type` is `"span_start"` / `"span_end"`, the `Span` field is valid.
- When `type` is `"metric"`, the `Metric` field is valid.

### 6.2 Full Example

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

### 6.3 Register and Use

After registering the factory in the extension's `main()`, users can reference the exporter in `~/.lcoder/observability.yaml`:

```yaml
exporter:
  name: stdout
```

> Note: exporter loading and configuration may evolve; refer to `pkg/observability/setup.go` and `configs/observability.yaml` for the latest details.

### 6.4 Common Exporter Patterns

- **File**: serialize each record as JSONL and append to a file.
- **Database**: e.g. SQLite exporter, insert records in batches or one by one.
- **Remote push**: e.g. Prometheus exporter, aggregate in memory and expose via HTTP.
- **Real-time analysis**: filter specific metrics and trigger alerts.

---

## 7. How to Add an HTTP Tool

HTTP tools require no Go code; declare them directly in `~/.lcoder/config.yaml`.

### 7.1 Minimal Example

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

### 7.2 Field Reference

| Field | Description |
|---|---|
| `name` | Tool name shown in the agent's tool list. |
| `endpoint` | POST endpoint that receives the request. |
| `description` | Tool description; helps the LLM decide when to call it. |
| `parameters` | JSON Schema parameter definition. |
| `execution_mode` | `parallel` or `serial`. |
| `headers` | Custom request headers; supports `${VAR}` environment variables. |

### 7.3 Implementing an HTTP Tool Endpoint

Your server receives a POST request with this JSON body:

```json
{
  "tool_call_id": "call_xxx",
  "name": "deploy",
  "arguments": {"service": "api"},
  "context": {"cwd": "/path/to/project"}
}
```

The response should be a tool result. The simplest form is a text content block:

```json
{
  "content": [{"type": "text", "text": "Deployment started"}]
}
```

---

## 8. How to Add an Agent Mode

An agent mode is a system prompt plus optional tool allow/deny lists. You can package it as a mode package.

### 8.1 Directory Structure

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

### 8.3 Mode Definition File

`agents/review.yaml`:

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

### 8.4 Install and Use

```bash
./lcoder install --local ./my-mode
./lcoder --mode review -p "review pkg/agent/loop.go"
```

### 8.5 Mode Configuration Fields

| Field | Description |
|---|---|
| `name` | Mode name; used with `--mode`. |
| `description` | Mode description shown by `./lcoder modes`. |
| `system_prompt` | Instructions injected into the system prompt. |
| `allowed_tools` | Allowed tools; if set, only these tools are available. |
| `denied_tools` | Denied tools. |
| `model` | Model ID for this mode (optional). |
| `provider` | Provider for this mode (optional). |
| `execution_mode` | `parallel` or `sequential` (optional). |

---

## 9. How to Connect an MCP Server

MCP servers are external tool services; Lcoder acts as the client. This section covers configuration and debugging, not MCP server development.

### 9.1 stdio MCP Server

```yaml
mcp_servers:
  - name: filesystem
    transport: stdio
    command: ["npx", "-y", "@modelcontextprotocol/server-filesystem", "."]
    env:
      NODE_ENV: production
```

### 9.2 SSE MCP Server

```yaml
mcp_servers:
  - name: remote-sse
    transport: sse
    url: http://localhost:3000
    headers:
      Authorization: Bearer ${REMOTE_MCP_TOKEN}
    timeout: 60
```

### 9.3 Streamable HTTP MCP Server

```yaml
mcp_servers:
  - name: remote-http
    transport: streamable-http
    url: https://mcp.example.com/v1
    headers:
      Authorization: Bearer ${REMOTE_MCP_TOKEN}
    timeout: 60
```

### 9.4 Debugging MCP

1. Verify the command is installed: `npx @modelcontextprotocol/server-filesystem --help`.
2. Check that the URL and port are reachable.
3. Look at Lcoder's startup logs for connection errors.
4. In the TUI, type `/mcp` to view connection status and tool lists.

### 9.5 MCP Tool Naming

MCP tools appear in the agent tool list as `{serverName}_{toolName}`, e.g. `filesystem_read_file`.

---

## 10. Subagent Extension

Lcoder has a built-in subagent mechanism. To use it, simply enable it in your config:

```yaml
subagent:
  enabled: true
```

Once enabled, the agent registers a `subagent` tool that can delegate work to other Lcoder agents. Cases that need a tailored runner can implement a custom subagent extension through the process-external extension runtime (`docs/superpowers/specs/2026-07-24-extension-runtime-design.md`); for normal use you do not need to install any extension.

### 10.1 Core Idea

The subagent extension registers a `subagent` tool whose parameters support three invocation styles:

- **Single**: one agent runs one task.
- **Parallel**: multiple agents run tasks in parallel.
- **Chain**: multiple agents run sequentially; later steps can reference `{previous}` results.

### 10.2 Extension Entry Point

```go
func New(cfg map[string]any) (extension.Extension, error) {
    return &subagentExtension{
        newRunner: subagent.NewRunner,
    }, nil
}
```

### 10.3 Tool Definition

The `subagent` tool uses `oneOf` to describe three mutually exclusive modes:

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

### 10.4 Usage

In conversation:

```text
Please use the subagent tool to have the worker agent analyze the structure of pkg/llm
```

## 11. Testing and Debugging

### 11.1 Unit Tests

Lcoder uses standard `go test`. Because `reference/Shannon` contains internal-package imports that break vet/test, exclude it:

```bash
go test $(go list ./... | grep -v 'reference/Shannon')
```

### 11.2 Integration Tests

Integration tests live in `test/integration/`, are gated by the `integration` build tag, and use the scripted `llmtest` client (no real API key required):

```bash
go test -tags integration ./test/integration -run TestAgentCrashCheckpointResume -v
```

### 11.3 Debugging Individual Components

- **Agent behavior**: add logging in `pkg/agent/loop.go`'s `run` method.
- **Context management**: use `pkg/contextmgr` tests to verify block selection and compaction.
- **Tool execution**: use `pkg/tools` tests.
- **LLM calls**: use `pkg/llm/llmtest` to mock the client.

### 11.4 Debugging with `lcoder --json`

JSON mode emits every event, making it easy to observe internal agent state:

```bash
./lcoder --json -p "Analyze main.go" 2>/dev/null | jq .
```

### 11.5 CI Flow

`.github/workflows/ci.yml` runs:

```bash
go build ./...
go vet ./...
go test ./... -count=1 -race
```

Locally, use the same commands but exclude `reference/Shannon`.

---

## 12. Contribution Guidelines and Code Conventions

### 12.1 Pre-Submit Checks

```bash
go build ./...
go vet $(go list ./... | grep -v 'reference/Shannon')
go test $(go list ./... | grep -v 'reference/Shannon') -count=1 -race
```

### 12.2 Code Style

- Match the surrounding code's comment density, naming, and idioms.
- Do not add features beyond what is requested.
- Do not create abstractions for code used only once.
- Do not modify unrelated adjacent code, comments, or formatting.
- When your changes leave orphaned code, remove unused imports, variables, and functions.

### 12.3 Documentation

- New config fields must include examples and comments in `configs/lcoder.yaml`.
- New CLI commands must be documented in `README.md` / `README_EN.md` and these guides.
- Complex logic should include comments explaining design intent.

### 12.4 Testing Requirements

- Bug fixes: write a reproducing test first, then fix.
- New features: include unit or integration tests.
- Refactors: ensure all tests pass before and after.

### 12.5 Commit Messages

A concise commit message format is recommended:

```
feat(contextmgr): add proactive compaction
fix(agent): resolve deadlock in tool execution
docs(user-guide): add MCP server examples
```

---

## 13. Extension API Quick Reference

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

> This is the in-process host interface. The Go plugin (`.so`) loading carrier is retired; process-external extensions should use the extension runtime (`pkg/extension/proto`/`runtime`/`bridge`), designed in `docs/superpowers/specs/2026-07-24-extension-runtime-design.md`.

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

### 13.7 Mode Definition File

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

### 13.8 Agent Builder Chain

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

### 13.9 Event Types

Common events:

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

Subscription example:

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

> This guide is based on the current Lcoder implementation. Extension APIs may evolve; always refer to the source code when developing.
