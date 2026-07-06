# Lcoder

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

## Architecture

See `.claude/CLAUDE.md` for project conventions and `docs/` for design notes and reports.

## License

MIT
