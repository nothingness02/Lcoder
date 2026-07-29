# Lcoder

[中文版本](README.md)

**Lcoder** is a software-engineering AI coding agent — pure Go, single binary, zero external runtime dependencies.

## Why Lcoder

| Feature | Lcoder | Others |
|---------|:---:|:---:|
| **Path Security Guard** | ✅ Sensitive-file detection + workspace boundary enforcement | Rare |
| **Single Binary** | ✅ `go build` — no Node, no Python | Most require Node.js |
| **Multi-Mode Agent** | ✅ code / plan / explore / review | Usually single-mode |
| **Session Branching** | ✅ Fork from any message, clone, retry | Few |
| **Checkpoint & Restore** | ✅ Crash-safe snapshots, exact state recovery | Rare |
| **Context Compaction** | ✅ Tiered compaction + cache-hit policy | Usually naive truncation |
| **Permission Engine** | ✅ 4-tier decision chain + audit log | Inconsistent |
| **Observability** | ✅ Prometheus / HTML / SQLite / Markdown | Few built-in |
| **Subagent Swarm** | ✅ Parallel subagents + swarm batching | Rare |
| **Deferred Tool Loading** | ✅ tool_search on-demand schema loading | Rare |
| **Extension System** | ✅ MCP + HTTP tools + extension bridges | Mixed MCP support |

## Quick Start

```bash
# Build — no runtime dependencies
go build -o lcoder ./cmd/lcoder

# Configure
mkdir -p ~/.lcoder
cp configs/lcoder.yaml ~/.lcoder/config.yaml

# Set API key (any compatible provider)
export OPENAI_API_KEY="sk-..."       # OpenAI-compatible
export ANTHROPIC_API_KEY="sk-ant-..." # Anthropic
export DEEPSEEK_API_KEY="sk-..."     # DeepSeek

# Start coding
./lcoder "Show me the project structure"
./lcoder                   # Interactive TUI
```

## Core Capabilities

### 🛡️ Path Security

Aligned with Kimi Code's security architecture. Every file tool (read/write/edit/ls/grep/find) passes through a unified path security guard before execution:

- **Sensitive-file blocking**: `.env`, SSH private keys, and credentials are permanently inaccessible to the agent
- **Workspace boundary**: relative `../` escapes are denied; external access requires explicit absolute paths
- **Pre-permission enforcement**: the guard runs before the permission engine — sensitive operations never prompt the user

### 🧠 Multi-Mode Agent

Four built-in modes, each with its own system prompt and tool constraints:

| Mode | Purpose | Tool Surface |
|------|---------|:---:|
| `code` | Day-to-day development | Full tools |
| `plan` | Design & analysis | Read-only + todo_write |
| `explore` | Codebase exploration | Read-only |
| `review` | Code review | Read-only |

```bash
./lcoder --mode plan "Design the authentication module"
./lcoder --mode review "Review pkg/agent/loop.go"
```

Custom modes supported (`.lcoder/modes/*.yaml`) with per-mode provider/model, tool rules, and exit approval.

### 📦 Skills

Encapsulate domain expertise as reusable Markdown skill packages:

```bash
.lcoder/skills/
├── security-review/SKILL.md   # Security review workflow
├── api-design/SKILL.md        # API design conventions
└── db-migration/SKILL.md      # Database migration guide
```

Skills can declare `allowed_tools` constraints, temporarily narrowing the agent's tool surface when activated.

### 🔀 Session Branching

Every message records a `parent_id` — one JSONL file represents an entire conversation tree:

```bash
./lcoder fork --session <id> --message <msg-id>   # Fork from any message
./lcoder clone --session <id>                     # Clone active branch
./lcoder -c                                       # Continue most recent session
```

### 💾 Checkpoints

Lightweight runtime snapshots are written automatically on crash (mode, model, turn count, context budget, etc.). Restoration recovers exact state without replaying message history.

### 📊 Context Management

Tiered context organization with intelligent compaction:

```
[system prompt] [mode prompt] [skills] [project docs] [recent messages]
                                                          ↑
                                          Dynamically allocated within TokenBudget
```

- Tiered compaction: system/project as stable layers, recent as sliding window
- `CompactThreshold` triggers proactive compaction before budget exhaustion
- `CacheHintPolicy` integrates deeply with Anthropic's prompt cache
- `DropThreshold` discards old messages under extreme pressure

### 🔧 Deferred Tool Loading

When the tool count is large (many MCP tools), ship only core tool schemas plus `tool_search`:

```yaml
context:
  deferred_tools: true
  core_tools: ["read", "write", "edit", "bash", "ls", "grep", "find"]
```

The model discovers tools on demand via `tool_search` and activates them with `tool_activate`. Saves first-token latency and preserves the provider cache prefix.

### 🐝 Subagent Swarm

```json
{
  "agent": "code",
  "items": [
    "Fix the bug in handler.go",
    "Add unit tests for service.go",
    "Update API documentation"
  ]
}
```

- **Parallel mode**: multiple subagents execute concurrently, results aggregate to the parent
- **Swarm mode**: batch-exclusive execution for large-scale parallel tasks
- Subagents inherit the parent's permissions and session context

### 📈 Observability

Multi-format export covering LLM calls, tool execution, latency, and more:

```bash
./lcoder stats <id>                           # Session statistics
./lcoder trace <id>                           # Human-readable trace
./lcoder export <id>                          # HTML report
./lcoder export <id> --format sqlite -o.db    # SQLite database
./lcoder export <id> --format prometheus -o.txt
./lcoder metrics                              # Prometheus endpoint (:9090)
```

### 🔐 Permission Engine

Four-tier decision chain with full audit logging:

```
guard policies → unsafe → deny rules → session approval → user rules → dangerous-default → fallback
```

- write / edit / bash default to ask; whitelisted commands auto-approve
- Approval scopes: once / project (written to `.lcoder/permissions.yaml`) / global
- Every decision recorded in the audit log, including unsafe-mode markers
- Glob-based matching with most-specific-wins semantics

### 🔌 Extension System

Three extension mechanisms working in concert:

**MCP Servers** (stdio / SSE / Streamable HTTP):
```yaml
mcp_servers:
  - name: codegraph
    transport: stdio
    command: ["codegraph", "serve", "--mcp", "--path", "."]
```

**HTTP Tools**:
```yaml
http_tools:
  - name: deploy
    endpoint: http://localhost:9001/deploy
    parameters:
      type: object
      properties:
        service: { type: string }
```

**Extension Bridges** (subprocess IPC for custom tools):
```yaml
extensions:
  bridges:
    - name: my-bridge
      command: ["./my-tool", "serve"]
```

### ⏱️ Tool Timeouts

Time-consuming tools accept LLM-controllable timeouts to prevent hangs:

- `bash`: `timeout` parameter (default 120s)
- MCP tools: `timeout_seconds` parameter (default 120s)

## Interface

```bash
./lcoder          # Interactive TUI
./lcoder -p "..." # One-shot
./lcoder -c       # Resume session
```

TUI keybindings:

| Key | Action |
|-----|--------|
| `Enter` | Send message |
| `Shift+Enter` | Newline |
| `Ctrl+O` | Expand/collapse tool output |
| `Ctrl+T` | Task sidebar |
| `Ctrl+B` | Fork from last message |
| `Ctrl+R` | Retry last message |
| `Ctrl+S` | Session picker |
| `Ctrl+L` | Clear chat |
| `Ctrl+M` | Extensions panel |
| `PgUp/PgDn` | Scroll history |

Slash commands: `/mcp` `/modes` `/tasks` `/tools` `/help`

## Architecture

```
cmd/lcoder/main.go
 └─ prepareAgent   config → LLM → tool/MCP registry → session store → observability → mode → context → Agent
 └─ runRoot        one-shot/JSON/TUI dispatch; writes checkpoint on crash

pkg/agent          Turn orchestration: steering → compact → streaming → tool exec → checkpoint
pkg/contextmgr     Tiered context: system/mode/skills/project/recent → dynamic token budgeting
pkg/llm            Engine routing/retry + OpenAI/Anthropic adapters + model catalog
pkg/tools          Tool registry + built-ins + deferred loading + HTTP/MCP tools
pkg/agent/hooks    Sensitive file detection + bash risk classification
pkg/permissions     4-tier permission chain + glob matching + rule persistence
pkg/session        JSONL branching storage
pkg/checkpoint     Lightweight runtime snapshots
pkg/tui            Bubble Tea TUI, event-bus-driven
pkg/events         Event bus
pkg/observability  JSONL/Prometheus/HTML/SQLite/Markdown export
pkg/config         koanf config loading with env-var overrides
```

## SWE-bench Lite Evaluation

The `eval/swe-bench-lite/` directory provides a Docker-based evaluation harness measuring initial and post-feedback resolution rates, with HTML/Markdown reports covering tokens, cache hits, tool chains, and per-module latency.

## License

MIT
