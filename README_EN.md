# Lcoder

[中文版本](README.md)

A minimal, extensible SWE agent harness.

- **Core**: Go
- **LLM engine**: in-process Go (hand-written HTTP+SSE adapters for OpenAI-compatible and Anthropic providers)
- **Extension tools**: HTTP servers and MCP servers (stdio, SSE, and Streamable HTTP)
- **UI**: Terminal UI via `charmbracelet/bubbletea`
- **Session storage**: JSONL with branching (`parent_id`)

## Quick Start

### 1. Build the Go CLI

```bash
go build -o lcoder ./cmd/lcoder
```

### 2. Configure

```bash
mkdir -p ~/.lcoder
cp configs/lcoder.yaml ~/.lcoder/config.yaml
# Edit ~/.lcoder/config.yaml and set your API keys via environment variables:
# OPENAI_API_KEY, ANTHROPIC_API_KEY, DEEPSEEK_API_KEY
```

### 3. Run

One-shot:

```bash
./lcoder -p "List files in the current directory"
# or pass the prompt as a positional argument
./lcoder "List files in the current directory"
```

Resume a session:

```bash
./lcoder -c                              # continue most recent session
./lcoder --session <id> -p "continue"    # resume a specific session
```

Interactive TUI:

```bash
./lcoder          # or ./lcoder tui
./lcoder tui --session <id>
```

Inside the TUI:
- `Enter` send message
- `Shift+Enter` newline
- `Ctrl+O` expand/collapse tool call results (full output + arguments)
- `Ctrl+T` toggle task sidebar
- `Ctrl+M` toggle extensions panel (HTTP tools / MCP servers)
- `Ctrl+S` session picker
- `Ctrl+B` fork from last assistant message
- `Ctrl+R` retry last assistant message
- `Ctrl+L` clear chat
- `PgUp/PgDn` or mouse wheel scroll history
- `Ctrl+C` / `Esc` quit

Slash commands while composing:
- `/mcp` manage configured MCP servers (reconnect / close)
- `/modes` switch agent mode
- `/tasks` toggle task sidebar
- `/tools` expand/collapse all tool results
- `/help` list all commands

List models:

```bash
./lcoder models
```

List agent modes (default modes are embedded, so this works from any directory):

```bash
./lcoder modes
```

Run with a specific mode:

```bash
./lcoder --mode plan -p "Design the auth module"
./lcoder --mode review -p "Review pkg/agent/loop.go"
```

## Project Context

Lcoder loads `AGENTS.md` and `CLAUDE.md` files from the current directory up to the filesystem root and appends them to the system prompt.

It also loads Markdown skills from `.lcoder/skills/<name>/SKILL.md` or `~/.lcoder/skills/<name>/SKILL.md` and injects them into the system prompt.

## Skills

Skills are Markdown packages in `.lcoder/skills/<name>/SKILL.md` or `~/.lcoder/skills/<name>/SKILL.md`.

List discovered skills:

```bash
./lcoder skills
```

A sample skill is provided in `configs/skills/security-review/`.

## Sessions

Sessions are stored as JSONL in `~/.lcoder/sessions/<project-hash>/`. Each message records a `parent_id`, so a single session file can represent a tree of branches.

```bash
./lcoder sessions                                  # list sessions
./lcoder -c                                        # continue most recent session
./lcoder --session <id>                            # resume a session
./lcoder fork --session <id> --message <msg-id>    # fork from a message
./lcoder clone --session <id>                      # clone active branch
```

## Security Defaults

Lcoder runs with a least-privilege posture by default. Destructive tools start in
"ask" mode and must be approved per invocation, per project, or globally.

- `write` and `edit` default to **ask** for every path.
- `bash` defaults to **ask**. A small built-in whitelist (e.g. `ls`, `pwd`,
  `echo`, `git status`, `git log`, `git diff`, `git branch`) is allowed without
  prompting.
- When a command is approved interactively you can choose:
  - **once** — allow this invocation only
  - **project** — remember the choice in `<repo>/.lcoder/permissions.yaml`
  - **global** — remember it in `~/.lcoder/permissions/global.yaml`
- Run with `--unsafe` to bypass the permission engine. Ultra-destructive
  commands such as `rm -rf /` still require approval.
- Every permission decision is recorded in the audit log, including
  `unsafe-allow` when `--unsafe` is active.

Rules follow glob patterns; more specific patterns win over generic ones. See
`configs/lcoder.yaml` for examples.

## Observability

Lcoder writes observability data to `~/.lcoder/observability/sessions/<session-id>.jsonl`.

```bash
./lcoder stats <id>              # session stats
./lcoder trace <id>              # human-readable trace
./lcoder export <id>             # export to HTML (default)
./lcoder export <id> --format sqlite -o report.db
./lcoder export <id> --format prometheus -o metrics.txt
./lcoder metrics                 # run Prometheus metrics endpoint on :9090
./lcoder metrics 9091            # run on :9091
```

Observed metrics include:

- LLM calls, input/output/total tokens, cache tokens, cost
- Tool execution count, duration, and errors
- Turn durations
- Total session duration

## Extension Tools

Lcoder supports two extension mechanisms:

1. **HTTP tools** — POST to a local or remote endpoint.
2. **MCP servers** — connect to Model Context Protocol servers over stdio, SSE, or the Streamable HTTP transport.

Example `~/.lcoder/config.yaml`:

```yaml
http_tools:
  - name: deploy
    endpoint: http://localhost:9001/deploy
    description: Deploy to staging
    parameters:
      type: object
      properties:
        service: { type: string }
      required: [service]
    execution_mode: parallel
    headers:
      Authorization: Bearer ${DEPLOY_TOKEN}

mcp_servers:
  - name: filesystem
    transport: stdio
    command: ["npx", "-y", "@modelcontextprotocol/server-filesystem", "."]

  - name: remote-sse
    transport: sse
    url: http://localhost:3000
    headers:
      Authorization: Bearer ${REMOTE_MCP_TOKEN}
    timeout: 60

  - name: remote-http
    transport: streamable-http
    url: https://mcp.example.com/v1
    headers:
      Authorization: Bearer ${REMOTE_MCP_TOKEN}
    timeout: 60
```

The `transport` field is required. MCP tools appear as `{serverName}_{toolName}` in the agent tool list.

In the TUI you can inspect and reconnect servers with `/mcp`.

## Tool Timeouts

Time-consuming tools accept an LLM-controllable timeout:

- `bash` has a `timeout` parameter (seconds, default **120**).
- MCP tools expose an optional `timeout_seconds` parameter (default **120**) when the server does not already define one.
- If the LLM omits the parameter, the default is used.

## Code Intelligence (MCP / codegraph)

Lcoder does not bundle a code index; it connects to external code-intelligence tools over MCP. The recommended companion is [codegraph](https://github.com/colbymchenry/codegraph): it parses the repo into a symbol/relation graph with tree-sitter (SQLite + FTS5) and exposes read-only MCP tools such as `codegraph_explore` (natural-language/keyword query → relevant symbol source, call paths, blast radius), `codegraph_search`, and `codegraph_files`.

To use it:

1. Install codegraph (a self-contained binary with a bundled Node runtime; see its README).
2. Run `codegraph init` once in the repo root to build the index (its serve process then keeps it incrementally updated via a file watcher).
3. Register the MCP server in `~/.lcoder/config.yaml`:

```yaml
mcp_servers:
  - name: codegraph
    transport: stdio
    command: ["codegraph", "serve", "--mcp", "--path", "."]
    env:
      CODEGRAPH_NO_DAEMON: "1"   # single process; lifecycle owned by lcoder
      CODEGRAPH_TELEMETRY: "0"   # disable anonymous telemetry
```

Once connected, the agent prefers these MCP tools for symbol/call-chain/impact exploration (the explore/plan/review mode prompts already guide it this way).

## SWE-bench Lite Evaluation

A dedicated evaluation harness for SWE-bench Lite is provided under `eval/swe-bench-lite/`. It runs Lcoder inside Docker containers, measures initial and post-feedback resolution rates, and generates HTML/Markdown reports with metrics such as token usage, cache hit rate, tool chains, and core-module performance.

See `eval/swe-bench-lite/README.md` (Chinese) or `eval/swe-bench-lite/README_EN.md` (English) for details.

## Architecture

```
cmd/lcoder/main.go
 └─ prepareAgent: config → LLM client → tool registry → MCP registry
                 → session store → observability → mode manager → context manager → Agent
 └─ runRoot: one-shot / JSON / TUI dispatch; writes a ReasonCrash checkpoint on SIGINT/SIGTERM

pkg/agent
 ├─ loop.go            Orchestrate turns: drain steering → compact → stream → execute tools → persist checkpoint
 ├─ streamer.go        Build turn requests, stream LLM events, assemble assistant messages
 ├─ executor.go        Validate, permission-check, and execute tool calls; owns deferred tool promotion
 └─ state.go           Runtime state, turn counter, steering/follow-up queues, abort

pkg/contextmgr
 └─ Manager            Organizes conversation into system/mode/skills/project_docs/recent blocks;
                       BuildTurnRequest selects blocks within TokenBudget, computes cache breakpoints,
                       injects ephemeral reminders, and MaybeCompactLeveled

pkg/llm
 ├─ engine             Routing and retry logic
 ├─ catalog            Model catalog and window/capability discovery
 ├─ provider           OpenAI-compatible and Anthropic HTTP+SSE adapters
 └─ client.go          Client facade exposed to the agent

pkg/tools
 └─ Registry           Collects tool definitions; built-ins live in pkg/tools/builtin;
                       HTTP/MCP tools are registered from config; supports deferred loading

pkg/events            Event bus: TurnStart/End, MessageStart/End, ToolExecutionStart/End, CompactionCommitted, etc.
pkg/session           JSONL session storage; reconstructs active branch via parent_id
pkg/checkpoint        Lightweight runtime snapshots (mode, model, turn, context budget/policy, steering queues);
                       does not store full messages
pkg/tui               Bubble Tea terminal UI; subscribes to the same event bus and handles permission Ask
pkg/config            koanf-based loading of ~/.lcoder/config.yaml with environment-variable overrides
```

For project conventions see `.claude/CLAUDE.md`; design notes and reports are in `docs/`.

## License

MIT
