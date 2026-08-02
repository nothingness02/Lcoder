# Lcoder

[中文版本](README.md)

**Lcoder** is an AI coding agent built in pure Go — single binary, zero dependencies, terminal-native.

> 🎯 **Best experience: interactive TUI.** Run `./lcoder` for a complete coding conversation — real-time streaming, tool-call visualization, session branching, and permission approval, all inside your terminal.

## Quick Start

### One-line Install (recommended)

```bash
# Install the latest release (auto-detects OS/arch, downloads prebuilt binary)
curl -fsSL https://raw.githubusercontent.com/nothingness02/Lcoder/master/install.sh | bash

# Install a specific version
curl -fsSL https://raw.githubusercontent.com/nothingness02/Lcoder/master/install.sh | bash -s -- --version v0.1.0

# Install from a local binary (no download)
./install.sh --binary /path/to/lcoder

# Launch after install (the script adds lcoder to your PATH automatically)
lcoder
```

The installer puts the binary in `~/.lcoder/bin/` and configures PATH (skip with `--no-modify-path`). Then set your API key and launch:

```bash
export ANTHROPIC_API_KEY="sk-ant-..."   # Anthropic (recommended)
export OPENAI_API_KEY="sk-..."          # OpenAI-compatible
export DEEPSEEK_API_KEY="sk-..."        # DeepSeek
lcoder
```

`~/.lcoder/config.yaml` is created automatically on first run. Press `Enter` to start chatting.

### Build from Source

```bash
# Only the Go toolchain required — no Node, no Python
go build -o lcoder ./cmd/lcoder
./lcoder
```

### Docker

```bash
# Clone the repo
git clone https://github.com/nothingness02/Lcoder.git
cd Lcoder/docker

# Create workspace directory (put your project here)
mkdir workspace

# Launch (first run builds the image, ~2 min)
ANTHROPIC_API_KEY="sk-ant-..." docker compose up
```

Docker handles Go compilation and containerization. `./workspace` is mounted as the working directory. Config and sessions persist via a named volume.

### Other Usage

```bash
./lcoder -p "List files in the current directory"   # One-shot (for scripting)
./lcoder -c                                         # Resume last session
./lcoder --mode plan -p "Design auth module"        # Specific mode
```

## TUI Guide

Lcoder's terminal interface is built around "conversation-driven programming." Describe what you need in natural language — the agent reads code, edits files, and runs commands automatically. You approve critical operations along the way.

### Layout

```
┌─ Lcoder · code · claude-sonnet-4-20250514 ── session: 3 ── turn: 12 ── 22K ─┐
│                                                                              │
│  You: Add rate-limiting middleware to handler.go                               │
│                                                                              │
│  Lcoder: Let me check the existing handler and project layout first.          │
│  │   read handler.go                                           expand ▼      │
│  │   ls pkg/middleware/                                                       │
│  │   write pkg/middleware/ratelimit.go    ✓ Wrote 1,247 bytes                │
│  │   Rate limiter created. Register it in handler.go to activate.            │
│                                                                              │
│  ▸ Type a message...                                       Ctrl+H help       │
└──────────────────────────────────────────────────────────────────────────────┘
```

### Keybindings

| Key | Action |
|-----|--------|
| `Enter` | Send message |
| `Shift+Enter` | Newline |
| `Ctrl+O` | Expand/collapse tool details |
| `Ctrl+T` | Task progress panel |
| `Ctrl+B` | Fork new session from last reply |
| `Ctrl+R` | Retry last reply |
| `Ctrl+S` | Browse & switch sessions |
| `Ctrl+M` | MCP / HTTP extensions panel |
| `Ctrl+L` | Clear session |
| `PgUp` / `PgDn` | Scroll history |

### Slash Commands

Type these in the input box:

| Command | Action |
|---------|--------|
| `/modes` | Switch agent mode (code / plan / explore / review) |
| `/mcp` | Manage MCP server connections |
| `/tasks` | Toggle task sidebar |
| `/tools` | Expand/collapse all tool results |
| `/help` | Show help |

## Why Lcoder

| Feature | Lcoder | Others |
|---------|:---:|:---:|
| **Single binary** | ✅ `go build` is all you need | Most need Node.js |
| **Path security** | ✅ Sensitive files + workspace guard | Rare |
| **Multi-mode** | ✅ code / plan / explore / review | Usually single-mode |
| **Session branching** | ✅ Fork, clone, retry from any message | Few |
| **Crash recovery** | ✅ Auto-saved checkpoints | Rare |
| **Context compaction** | ✅ Tiered + cache-hit policy | Usually truncation |
| **Permission engine** | ✅ 4-tier chain + audit log | Inconsistent |
| **Observability** | ✅ Prometheus / HTML / SQLite | Rare |
| **Sub-agents** | ✅ Parallel + swarm mode | Few |
| **Extensions** | ✅ MCP + HTTP tools + bridges | Mixed MCP support |

## Core Capabilities

### 🧠 Multi-Mode Agent

Switch modes in the TUI with `/modes`. Each mode has its own prompt and tool constraints:

| Mode | Purpose | Tools |
|------|---------|:---:|
| `code` | Day-to-day development | Full |
| `plan` | Design & analysis | Read-only + todo |
| `explore` | Codebase exploration | Read-only |
| `review` | Code review | Read-only |

Custom modes (`.lcoder/modes/*.yaml`) support per-mode provider/model, tool rules, and exit approval.

### 🛡️ Path Security Guard

Aligned with Kimi Code. Every file operation passes through security validation before execution:

- `.env`, SSH keys, and credentials are permanently blocked
- `../` path escapes are rejected; external files require absolute paths
- Guard runs before the permission engine — no user prompts for blocked operations

### 📦 Skills

Drop Markdown files into `.lcoder/skills/` to add domain knowledge:

```
.lcoder/skills/
├── security-review/SKILL.md   # Security review workflow
├── api-design/SKILL.md        # API design conventions
└── db-migration/SKILL.md      # Database migration guide
```

The TUI suggests matching skills as you type.

### 🔀 Session Management

Every message has a `parent_id`, enabling full branching:

- `Ctrl+B` fork a new session from any reply
- `Ctrl+S` browse and restore past sessions
- `Ctrl+R` retry with a different approach
- `-c` flag resumes the most recent session

### 🔐 Permission Engine

Write/edit/bash operations require approval. The TUI shows a confirmation dialog:

```
┌─ Approve ──────────────────────────────────────────┐
│  bash: go test ./...                                 │
│                                                      │
│  [y] once  [a] project  [g] global  [n] deny         │
└──────────────────────────────────────────────────────┘
```

Three scopes: once / project / global. All decisions are audit-logged.

### 🔌 Extensions

Register in `~/.lcoder/config.yaml` to extend the agent:

```yaml
# MCP servers (codegraph recommended for code intelligence)
mcp_servers:
  - name: codegraph
    transport: stdio
    command: ["codegraph", "serve", "--mcp", "--path", "."]

# HTTP tools
http_tools:
  - name: deploy
    endpoint: http://localhost:9001/deploy
    parameters:
      type: object
      properties:
        service: { type: string }
```

### 🐝 Sub-Agent Swarm

Describe parallel tasks in conversation — the agent splits and executes automatically:

> Do three things at once: fix the handler.go bug, add tests for service.go, update the README

Sub-agents run concurrently with live progress in the TUI. Results are auto-merged.

### 📊 Context Management

Tiered conversation organization with intelligent compaction:

- Stable layers (system prompt / project docs / skills) always retained
- Sliding window (recent messages) adjusted dynamically by token budget
- Anthropic prompt cache integration reduces repeat billing

### 📈 Observability

```bash
./lcoder stats <id>                    # Session statistics
./lcoder export <id>                   # HTML report
./lcoder export --format sqlite -o.db  # SQLite database
./lcoder metrics                       # Prometheus endpoint (:9090)
```

## Architecture

```
Lcoder (single binary)
├── TUI (Bubble Tea)        Terminal interface
├── Agent                   Turn orchestration
│   ├── Permissions         4-tier decision chain
│   ├── Context Manager     Tiered token budget
│   └── Checkpoints         Auto-save on crash
├── LLM Engine              OpenAI / Anthropic / DeepSeek
├── Tool System
│   ├── Built-in tools      read write edit bash ls grep find
│   ├── MCP Client          stdio / SSE / Streamable HTTP
│   ├── HTTP Tools          Custom REST endpoints
│   └── Extension Bridges   Subprocess IPC
└── Observability           JSONL / Prometheus / HTML / SQLite
```

## License

MIT
