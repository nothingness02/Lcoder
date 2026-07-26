# Lcoder User Guide

> This document is the detailed user manual for Lcoder, aimed at end users who are using Lcoder for the first time. If you are a developer looking to extend Lcoder, please read `DEVELOPER_GUIDE_EN.md`.

## Table of Contents

1. [Introduction](#1-introduction)
2. [Installation and Requirements](#2-installation-and-requirements)
3. [Initial Configuration](#3-initial-configuration)
4. [Quick Start](#4-quick-start)
5. [CLI Command Reference](#5-cli-command-reference)
6. [TUI In-Depth](#6-tui-in-depth)
7. [Agent Modes](#7-agent-modes)
8. [Skills](#8-skills)
9. [Sessions and Branching](#9-sessions-and-branching)
10. [Security and Permissions](#10-security-and-permissions)
11. [Extension Tools](#11-extension-tools)
12. [Code Index](#12-code-index)
13. [Observability](#13-observability)
14. [Troubleshooting](#14-troubleshooting)
15. [Configuration Field Reference](#15-configuration-field-reference)
16. [Appendix: Environment Variables and File Paths](#16-appendix-environment-variables-and-file-paths)

---

## 1. Introduction

Lcoder is a **minimal, extensible SWE (Software Engineering) agent runtime framework** written in Go. It combines a large language model (LLM), tool calling, session management, permission control, and observability into a single command-line program. You can use it as a daily coding assistant or as a foundation for more complex agent systems.

### 1.1 Core Features

- **In-process LLM engine**: Communicates directly with OpenAI-compatible and Anthropic endpoints through hand-written HTTP+SSE adapters, with no external SDK dependency.
- **Multi-mode agent**: Built-in modes such as `code`, `plan`, `explore`, `review`, and `test` optimize the system prompt for different scenarios.
- **Rich tool ecosystem**:
  - Built-in tools: file read/write, edit, `bash`, code-index search, memory, subagent, etc.
  - HTTP tools: send POST requests to arbitrary endpoints.
  - MCP servers: connect to Model Context Protocol servers over stdio, SSE, or Streamable HTTP.
- **Terminal UI (TUI)**: Based on `charmbracelet/bubbletea`, supporting message history, tool result folding/expansion, session picker, and extensions panel.
- **Sessions and branching**: Sessions are saved as JSONL; each message records a `parent_id`, enabling branching from any message.
- **Secure by default**: Destructive tools require confirmation by default, with `allow/ask/deny` permission rules.
- **Observability**: Automatically records LLM calls, token usage, tool execution, latency, etc., with export to HTML / SQLite / Prometheus.
- **Code index**: Optional SQLite-backed code graph index supporting Go / TypeScript / JavaScript / Python, with automatic context injection.

### 1.2 When to Use Lcoder

- Daily coding: explain, refactor, complete, and debug code.
- Code review: ask the agent to review specified files.
- Design: use `plan` mode for high-level architecture discussions.
- Automated workflows: integrate Lcoder into CI/CD, deployment, documentation generation, etc. via HTTP tools or MCP servers.
- Custom agents: build your own tools, modes, hooks, and exporters on top of Lcoder.

### 1.3 Differences from Similar Tools

| Dimension | Lcoder | Claude Code / Cursor / etc. |
|---|---|---|
| Deployment | Self-hosted from source | Usually closed-source client or IDE plugin |
| Extension mechanism | Process-external extensions, HTTP tools, MCP servers | Mostly vendor-provided |
| Model routing | In-process engine, any OpenAI-compatible endpoint | Usually official models only |
| Session storage | JSONL, inspectable, forkable, backup-friendly | Usually proprietary storage |
| Permission control | Fine-grained glob rules, local audit log | Determined by the product |

---

## 2. Installation and Requirements

### 2.1 Prerequisites

- **Go**: Go 1.25.4 or later (as declared in `go.mod`).
- **Operating system**: Linux, macOS, and Windows are all supported. The TUI requires a terminal that supports ANSI escape sequences.
- **API key**: At least one LLM provider API key (OpenAI, Anthropic, DeepSeek, Moonshot, DashScope, etc.).

### 2.2 Build from Source

```bash
# Clone the repository
git clone https://github.com/lcoder/lcoder.git
cd lcoder

# Build the binary into the current directory
go build -o lcoder ./cmd/lcoder

# Verify
./lcoder --help
```

On Windows, the command above produces `lcoder.exe`.

### 2.3 Install to PATH (Optional)

```bash
# Linux / macOS
mkdir -p ~/.local/bin
cp lcoder ~/.local/bin/
# Make sure ~/.local/bin is in your PATH

# Windows (PowerShell)
# Copy lcoder.exe to a directory already on PATH, e.g. C:\Tools
```

### 2.4 Upgrading

Lcoder does not currently provide an auto-update command. To upgrade, pull the latest source and run `go build` again.

---

## 3. Initial Configuration

Lcoder loads configuration in the following order:

1. Built-in defaults (`config.DefaultConfig()`).
2. `~/.lcoder/config.yaml` (or the file specified by `--config`).
3. Environment variable overrides (`LCODER_` prefix, see field-specific notes).
4. Command-line flags: `--model`, `--provider`, `--unsafe`, etc.

### 3.1 Copy the Example Configuration

```bash
mkdir -p ~/.lcoder
cp configs/lcoder.yaml ~/.lcoder/config.yaml
```

### 3.2 Choose a Provider and Model

Edit `~/.lcoder/config.yaml`:

```yaml
provider: openai
model: gpt-4o-mini
```

`provider` can be `openai`, `anthropic`, `deepseek`, `moonshot`, `dashscope`, etc. If `provider` is omitted, Lcoder tries to resolve the provider automatically from the `model` id.

### 3.3 Set the API Key

**Method 1: Environment variable (recommended, simplest)**

```bash
export OPENAI_API_KEY=sk-...
export ANTHROPIC_API_KEY=sk-...
export DEEPSEEK_API_KEY=sk-...
```

Windows PowerShell:

```powershell
$env:OPENAI_API_KEY = "sk-..."
```

**Method 2: `~/.lcoder/credentials.yaml`**

This file is written automatically by the TUI setup wizard with permissions `0600`.

```yaml
openai:
  api_key: sk-...
  # base_url: https://api.openai.com/v1   # optional
```

**Method 3: Declare the provider and embed the key in `config.yaml`**

```yaml
providers:
  moonshot:
    base_url: "https://api.moonshot.cn/v1"
    api_key: "{env:MOONSHOT_API_KEY}"
```

`{env:VAR}` is read from the environment at startup.

**Priority**: `config.providers.<name>.api_key` (including `{env:VAR}`) > `~/.lcoder/credentials.yaml` > standard environment variables such as `OPENAI_API_KEY`.

### 3.4 First-Run TUI Wizard

If the current provider has no API key configured, the TUI shows a setup wizard on first launch:

1. Select a provider.
2. Select a model.
3. Enter the API key.

The key is then written to `~/.lcoder/credentials.yaml`.

### 3.5 Verify the Configuration

```bash
./lcoder models          # List available models
./lcoder -p "Hello"      # Run a one-shot conversation
```

If the model responds, the configuration is working.

---

## 4. Quick Start

### 4.1 Run a One-Shot Conversation

A one-shot conversation executes a single user prompt and exits, useful for scripting.

```bash
./lcoder -p "List files in the current directory"

# Or use a positional argument
./lcoder "List files in the current directory"
```

Only the assistant's final reply is printed. Tool execution details are visible in TUI or JSON mode.

### 4.2 Start the Interactive TUI

Launch the TUI by running `lcoder` without arguments:

```bash
./lcoder
# Or explicitly
./lcoder tui
```

In the TUI:

- Type a prompt and press `Enter` to send.
- Press `Shift+Enter` to insert a newline in the input box.
- Press `Ctrl+C` or `Esc` to quit.

### 4.3 Resume a Session

Lcoder saves sessions automatically. After exiting, resume with:

```bash
./lcoder -c                                  # Continue the most recent session
./lcoder --session <id> -p "Continue"        # Resume a specific session and send a new message
./lcoder --session <id>                     # Resume a specific session in the TUI
```

### 4.4 Use a Mode

Modes change the system prompt and therefore the agent's behavior:

```bash
./lcoder --mode plan -p "Design an authentication module"
./lcoder --mode review -p "Review pkg/agent/loop.go"
./lcoder --mode test -p "Write unit tests for pkg/llm/client.go"
```

Use `./lcoder modes` to list available modes.

### 4.5 A Complete Example

```bash
# 1. Configure and verify
export OPENAI_API_KEY=sk-...
./lcoder models | grep gpt-4o

# 2. Ask for a design in plan mode
./lcoder --mode plan -p "How should I add a caching layer to this project?"

# 3. Enter TUI to refine the plan
./lcoder -c

# 4. Ask the agent to implement it
# In the TUI: "Please implement an LRU cache under pkg/cache based on the plan above"
```

---

## 5. CLI Command Reference

### 5.1 Global Flags

The following flags belong to the main `lcoder` command and **cannot** be used on subcommands such as `tui`. For example, `./lcoder tui --session <id>` does not work; specify the session on the main command instead:

```bash
./lcoder --session <id>       # Resume the specified session in the TUI
./lcoder -c                   # Continue the most recent session in the TUI
```

| Flag | Description |
|---|---|
| `--config PATH` | Path to the config file; default is `~/.lcoder/config.yaml`. |
| `--model ID` | Temporarily override the model in the config file. |
| `--provider NAME` | Temporarily override the provider in the config file. |
| `--session ID` | Load the specified session. |
| `-c, --continue` | Continue the most recent session. |
| `--mode NAME` | Specify the agent mode; default is `code`. |
| `-p, --prompt TEXT` | Pass a single prompt and exit after execution. |
| `--json` | Output events as JSONL instead of TUI/text. |
| `--unsafe` | Bypass the permission engine; ultra-destructive commands still require approval. |

> Only `--unsafe` is a persistent flag and can be used on subcommands such as `tui`.

### 5.2 Subcommands

| Command | Usage | Description |
|---|---|---|
| `models` | `./lcoder models` | List available models from the catalog. |
| `skills` | `./lcoder skills` | List discovered skills in the current directory and `~/.lcoder/skills`. |
| `sessions` | `./lcoder sessions` | List sessions for the current workspace. |
| `modes` | `./lcoder modes` | List available agent modes. |
| `stats` | `./lcoder stats <session-id>` | Show stats for a session. |
| `trace` | `./lcoder trace <session-id>` | Print a human-readable trace for a session. |
| `export` | `./lcoder export <session-id>` | Export session observability data; default is HTML. |
| `metrics` | `./lcoder metrics [port]` | Run the Prometheus metrics endpoint; default port `:9090`. |
| `tui` | `./lcoder tui` | Start the interactive TUI. To resume a session, use `./lcoder --session ID` or `./lcoder -c`. |
| `install` | `./lcoder install SOURCE` | Install an extension or package. |
| `uninstall` | `./lcoder uninstall NAME` | Uninstall an extension or package. |
| `list-extensions` | `./lcoder list-extensions` | List installed extensions and packages. |
| `update` | `./lcoder update NAME` | Update an installed extension or package. |

### 5.3 `export` Command Details

```bash
# Default HTML export
./lcoder export <session-id>

# Specify a format
./lcoder export <session-id> --format sqlite -o report.db
./lcoder export <session-id> --format markdown -o report.md
./lcoder export <session-id> --format prometheus -o metrics.txt
```

### 5.4 `install` Command Details

```bash
# Install a Go extension from a local directory
./lcoder install ./my-extension --name my-ext --local

# Install from a git repository
./lcoder install https://github.com/acme/lcoder-ext-tools.git --name acme-tools

# Install a package (e.g. a mode pack)
./lcoder install ./acme-modes --name acme-modes --local
```

> `./lcoder install` only copies files to `~/.lcoder/extensions/` or `~/.lcoder/packages/`; it does **not** auto-register extensions. Process-external extensions are discovered automatically from `~/.lcoder/extensions/` (global) or `.lcoder/extensions/` (project level), each with an `extension.yaml` manifest; see `docs/superpowers/specs/2026-07-24-extension-runtime-design.md`.

### 5.5 `sessions` and Session Recovery

```bash
./lcoder sessions
# Example output:
# abc123  2026-07-10 14:32
# def456  2026-07-11 09:15

./lcoder --session abc123 -p "Continue yesterday's work"
```

## 6. TUI In-Depth

The TUI (Terminal User Interface) is Lcoder's default interaction mode, built on `charmbracelet/bubbletea`.

### 6.1 Launch and Quit

```bash
./lcoder              # Start a new session
./lcoder tui          # Explicitly start the TUI
./lcoder -c           # Continue the most recent session in the TUI
./lcoder --session <id>   # Resume the specified session in the TUI
```

To quit:

- `Ctrl+C`: Sends SIGINT; Lcoder writes a crash checkpoint and exits.
- `Esc`: Usually used to exit or cancel the current operation.

### 6.2 Keyboard Shortcuts

| Shortcut | Action |
|---|---|
| `Enter` | Send the current input message. |
| `Shift+Enter` | Insert a newline in the input box. |
| `Ctrl+O` | Expand or collapse the most recent tool call result. |
| `Ctrl+T` | Toggle the task sidebar. |
| `PgUp` / `PgDn` | Scroll through message history. |
| Mouse wheel | Scroll through message history (requires terminal mouse support). |
| `Esc` | Close a panel, cancel the current operation, or exit. |

> The following features are currently available through slash commands and have no default keyboard shortcuts: session picker (`/sessions`), extensions panel (`/extensions` or `/mcp`), retry (`/retry`), and new chat (`/new`).

### 6.3 Slash Commands

Type the following commands in the input box for quick actions:

| Command | Action |
|---|---|
| `/mcp` | Manage configured MCP servers: view status, reconnect, close. |
| `/modes` | Switch the current agent mode. |
| `/tasks` | Toggle the task sidebar. |
| `/tools` | Expand or collapse all tool results. |
| `/help` | List all available commands. |

### 6.4 Viewing Tool Results

When the assistant invokes a tool, the TUI shows a tool-call card. Press `Ctrl+O` to expand it and view:

- Tool name and call ID.
- Passed arguments.
- Execution output or error.
- Execution duration.

### 6.5 Permission Confirmation UI

When a tool call matches an `ask` rule, the TUI shows a confirmation dialog with three options:

- **once**: Allow only this invocation.
- **project**: Allow the rule and write it to `<repo>/.lcoder/permissions.yaml`.
- **global**: Allow the rule and write it to `~/.lcoder/permissions/global.yaml`.

### 6.6 Session Picker

Type `/sessions` to open the session picker. You can:

- View all sessions for the current workspace.
- Press `Enter` to switch to the selected session.
- Press `Esc` to cancel.

### 6.7 Extensions Panel

Type `/extensions` or `/mcp` to open the extensions panel, which shows:

- Configured HTTP tools.
- Connected MCP servers and their tool lists.
- Server connection status.

---

## 7. Agent Modes

Modes are sets of system prompts and behavior tuned for specific tasks. Lcoder loads built-in modes as well as custom modes from `<repo>/.lcoder/modes/` and `~/.lcoder/modes/`.

### 7.1 Built-In Modes

| Mode | Purpose |
|---|---|
| `code` | Default mode for daily coding, refactoring, and debugging. |
| `plan` | Design mode for architecture discussions and task breakdown. |
| `explore` | Exploration mode for reading unfamiliar codebases. |
| `review` | Review mode for reviewing files or commits. |
| `test` | Testing mode for writing, analyzing, and running unit tests. |

### 7.2 List Available Modes

```bash
./lcoder modes
```

Example output:

```
- code: General coding assistant
- plan: Architecture and planning
- explore: Codebase exploration
- review: Code review
- test: Testing assistant
```

### 7.3 Run with a Mode

```bash
./lcoder --mode plan -p "Design the database schema for an order system"
./lcoder --mode review -p "Please review pkg/session/store.go"
./lcoder --mode explore
```

If not specified, the default mode is `code`.

### 7.4 Custom Modes

Custom modes are YAML files placed in:

- `<repo>/.lcoder/modes/`
- `~/.lcoder/modes/`

Each mode is a standalone `.yaml` file, such as `review.yaml`, loaded by `agent.ModeManager` at startup. For more details, see `DEVELOPER_GUIDE_EN.md`.

---

## 8. Skills

Skills are Markdown instruction packages loaded on demand to give the agent extra instructions, templates, or background knowledge for specific scenarios. At startup only each skill's `name + description` enters the system prompt (the catalog); the full body is loaded only when the skill is activated.

### 8.1 Skill File Locations

Lcoder loads skills from:

- `<repo>/.lcoder/skills/<name>/SKILL.md`
- `~/.lcoder/skills/<name>/SKILL.md`

A skill package is a directory containing at least `SKILL.md`; related files can be placed in the same directory.

### 8.2 List Discovered Skills

```bash
./lcoder skills
```

Example output:

```
- security-review: Perform security-focused code review
- commit-message: Generate conventional commit messages
```

### 8.3 How Skills Are Activated

**Model-driven activation (default)**: the catalog in the system prompt lists every skill's name and purpose. When the model decides the request matches a skill, it calls the `use_skill` tool; the skill body enters the conversation as that tool's result and flows naturally with the turns.

**Manual trigger**: in a one-shot conversation, force activation with `/skill:name`:

```bash
./lcoder -p "/skill:security-review Please review pkg/auth/jwt.go"
```

In the TUI, you can also type `/skill:security-review` in the input box. A manual trigger folds the skill body into that user message — the same content the model-driven path sees.

### 8.4 Restricting Tools with allowed_tools

A skill can declare `allowed_tools` in its frontmatter to restrict which tools may be called while it is active:

```markdown
---
name: security-review
description: Review code for security vulnerabilities
allowed_tools:
  - read
  - grep
  - ls
  - find
---
```

While active, calls to tools outside the list are rejected at execution time (`use_skill` itself always stays available, so the model can switch to another skill; activating a skill without `allowed_tools` lifts the restriction). The restriction lives in the current process only and is not written to checkpoints.

### 8.5 Example Skill

The repository includes a sample skill:

```
configs/skills/security-review/
├── SKILL.md
└── ...
```

Copy it to `~/.lcoder/skills/` or a local `.lcoder/skills/` directory to try it out.

---

## 9. Sessions and Branching

### 9.1 Session Storage Location

Sessions are stored as JSONL in:

```
~/.lcoder/sessions/<project-hash>/<session-id>.jsonl
```

The `<project-hash>` is derived from the current working directory so that sessions for the same project are grouped together.

### 9.2 Session File Structure

Each message is one line of JSON containing:

- `id`: Unique message ID.
- `parent_id`: Parent message ID, used to represent branch relationships.
- `role`: `user`, `assistant`, or `tool`.
- `content`: Message content.

Thanks to `parent_id`, a single JSONL file can represent a tree of messages and support branching from any node.

### 9.3 List Sessions

```bash
./lcoder sessions
```

Output includes the session ID and creation time:

```
abc123  2026-07-10 14:32
def456  2026-07-11 09:15
```

### 9.4 Resume a Session

```bash
./lcoder -c                                    # Continue the most recent session
./lcoder --session abc123 -p "Continue work"   # Resume a specific session and send a new message
./lcoder --session abc123                      # Resume the specified session in the TUI
```

### 9.5 Fork and Retry

Lcoder session data supports branching through `parent_id`. The TUI does not currently provide a default keyboard shortcut for forking; you can create branches manually by editing the session JSONL file, or programmatically via the `pkg/session` API.

Type `/retry` to retry the most recent assistant message.

### 9.6 Backup and Cleanup

Session files are plain-text JSONL and can be copied directly for backup:

```bash
cp ~/.lcoder/sessions/<project-hash>/<session-id>.jsonl ./session-backup.jsonl
```

To clean up old sessions, simply delete the corresponding JSONL files.

---

## 10. Security and Permissions

Lcoder runs with a least-privilege posture by default. Tools that can damage code, files, or systems request confirmation on first use.

### 10.1 Default Permission Rules

The default rules in `configs/lcoder.yaml` are:

```yaml
permissions:
  rules:
    read:
      "*": allow
    write:
      "*": ask
    edit:
      "*": ask
    bash:
      "*": ask
      "ls": allow
      "ls *": allow
      "pwd": allow
      # ... other safe commands
      "rm -rf /": deny
      "sudo *": deny
```

### 10.2 Rule Actions

Each rule can be set to:

- `allow`: Execute without confirmation.
- `ask`: Require confirmation on every invocation.
- `deny`: Always reject.

### 10.3 Glob Matching and Priority

Rules use glob patterns to match commands or paths. More specific patterns take precedence over more general ones.

Example:

```yaml
bash:
  "*": ask
  "git status": allow
  "rm -rf /": deny
```

- `git status` matches `allow`.
- `rm -rf /` matches `deny`.
- All other commands match `ask`.

### 10.4 Interactive Approval Levels

When a confirmation dialog appears, you can choose:

- **once**: Allow only this invocation; do not write any rule file.
- **project**: Write the rule to `<repo>/.lcoder/permissions.yaml`; applies to the current project only.
- **global**: Write the rule to `~/.lcoder/permissions/global.yaml`; applies to all projects.

### 10.5 `--unsafe` Mode

```bash
./lcoder --unsafe -p "Execute some dangerous operation"
```

`--unsafe` bypasses the permission engine, but the following ultra-destructive commands are still blocked:

- `rm -rf /`
- `sudo *`
- `mkfs.*`
- `dd`
- `reboot`, `shutdown`, `halt`
- Fork bombs, etc.

### 10.6 Audit Log

Every permission decision is written to the audit log at:

```
~/.lcoder/observability/sessions/<session-id>.jsonl
```

This includes `unsafe-allow` decisions when `--unsafe` is active.

### 10.7 Example Permission File

Project-level permissions in `<repo>/.lcoder/permissions.yaml`:

```yaml
rules:
  bash:
    "go test ./...": allow
    "go build ./...": allow
  write:
    "*.go": ask
    "*.md": allow
```

The global permission file `~/.lcoder/permissions/global.yaml` uses the same format.

## 11. Extension Tools

Lcoder supports three ways to extend the tools available to the agent:

1. **Built-in tools**: Provided by Lcoder, such as `read`, `write`, `edit`, `bash`, `memory`, `repo_index`, etc. `subagent` is registered only when explicitly enabled in the configuration.
2. **HTTP tools**: Expose arbitrary HTTP endpoints as tools through configuration.
3. **Process-external extensions**: Standalone processes that talk to the host over stdio JSON-RPC; they are discovered automatically from `~/.lcoder/extensions/` (global) or `.lcoder/extensions/` (project level), each with an `extension.yaml` manifest.

> **Note**: `./lcoder install` only copies an extension or package to `~/.lcoder/extensions/` or `~/.lcoder/packages/`; it does **not** auto-register extensions. Process-external extensions are discovered from the directories above and need no declaration in `tool_extensions`; HTTP tools take effect simply by adding them under `http_tools`.

### Enabling Subagent

```yaml
subagent:
  enabled: true
```

### Registering Extensions

`tool_extensions` currently supports only `type: json`: `path` points to a JSON descriptor file that defines an HTTP tool (`name`/`endpoint`/`parameters`, etc., equivalent to `http_tools`):

```yaml
tool_extensions:
  - name: weather
    type: json
    path: ~/.lcoder/tools/weather.json
```

Process-external extensions are not configured via `tool_extensions`; they are auto-discovered from `~/.lcoder/extensions/` (global) or `.lcoder/extensions/` (project level). See `docs/superpowers/specs/2026-07-24-extension-runtime-design.md` for the design.

### 11.1 HTTP Tools

HTTP tools send a POST request to the configured endpoint when the agent needs them and return the response as the tool result.

Configure in `~/.lcoder/config.yaml`:

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

Field descriptions:

| Field | Description |
|---|---|
| `name` | The tool name shown to the agent. |
| `endpoint` | POST request target URL. |
| `description` | Tool description; influences when the LLM invokes it. |
| `parameters` | JSON Schema parameter definition. |
| `execution_mode` | `parallel` or `serial`; determines whether the tool can run in parallel. |
| `headers` | Custom request headers; supports `${VAR}` environment variable interpolation. |

Environment variable interpolation:

```yaml
headers:
  Authorization: "Bearer ${DEPLOY_TOKEN}"
```

Lcoder reads the `DEPLOY_TOKEN` environment variable at startup and substitutes it.

### 11.2 HTTP Tool Endpoint Contract

Your endpoint receives a POST request with a JSON body containing the following fields:

```json
{
  "tool_call_id": "call_xxx",
  "name": "deploy",
  "arguments": {"service": "api"},
  "context": {"cwd": "/path/to/project"}
}
```

| Field | Description |
|---|---|
| `tool_call_id` | Unique ID for this tool call. |
| `name` | Name of the invoked tool. |
| `arguments` | Object containing the arguments provided by the agent. |
| `context.cwd` | Current working directory of the Lcoder process. |

The response should be a tool result. The simplest form is a text response:

```json
{
  "content": [{"type": "text", "text": "Deployment started"}]
}
```

Responses may also include `details` and `terminate` fields.

### 11.3 MCP Servers

MCP (Model Context Protocol) is a standard protocol that lets Lcoder connect to external tool servers. Lcoder supports three transports:

- **stdio**: Communicates over a subprocess's stdin/stdout; suitable for local tools.
- **sse**: Server-Sent Events; suitable for remote servers.
- **streamable-http**: Streamable HTTP; suitable for remote servers.

Configuration example:

```yaml
mcp_servers:
  - name: filesystem
    transport: stdio
    command: ["npx", "-y", "@modelcontextprotocol/server-filesystem", "."]
    env:
      NODE_ENV: production

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

Field descriptions:

| Field | Description |
|---|---|
| `name` | Server name; MCP tools appear as `{serverName}_{toolName}`. |
| `transport` | Transport type: `stdio`, `sse`, or `streamable-http`. |
| `command` | Command to start the server in stdio mode. |
| `url` | Server URL for sse / streamable-http modes. |
| `headers` | HTTP headers used when connecting. |
| `env` | Environment variables passed to the stdio subprocess. |
| `timeout` | Connection timeout in seconds. |

### 11.4 Managing MCP in the TUI

Type `/mcp` to open the MCP management panel, where you can:

- View the connection status of each server.
- See the list of tools provided by each server.
- Reconnect or close individual servers.

### 11.5 Tool Timeouts

For long-running operations, Lcoder lets the LLM control the timeout:

- The `bash` tool provides a `timeout` parameter; default is **120 seconds**.
- MCP tools expose an optional `timeout_seconds` parameter when the server does not define one; default is **120 seconds**.

If the LLM does not specify a timeout, the default is used.

---

## 12. Code Index

The code index is an optional Lcoder feature that provides richer repository context. It builds a SQLite-backed code graph in the background and supports searching related symbols by call, reference, containment, inheritance, and other relationships.

### 12.1 Enable the Code Index

Configure in `~/.lcoder/config.yaml`:

```yaml
code_index:
  enabled: true
  auto_inject: false
  max_results: 10
  max_tokens: 8192
  languages: [go, python, javascript, typescript]
  exclude:
    - ".git/**"
    - ".claude/**"
    - "reference/**"
    - "vendor/**"
    - "node_modules/**"
```

Field descriptions:

| Field | Description |
|---|---|
| `enabled` | Whether to enable the code index. |
| `auto_inject` | Whether to automatically inject relevant code context on each user turn. |
| `max_results` | Maximum number of results per query. |
| `max_tokens` | Maximum token budget for injected context. |
| `languages` | Languages to index: `go`, `python`, `javascript`, `typescript`. |
| `exclude` | Glob list of paths to exclude. |

### 12.2 First Run and Incremental Updates

On first enable, Lcoder performs a full project scan and builds the index. After that:

- Only changed, added, or deleted files are re-parsed by comparing mod-time and size.
- If `watch: true`, file changes automatically trigger index updates with debouncing.
- Index resources are cleaned up on process exit.

### 12.3 The `repo_index` Tool

With the code index enabled, the agent can actively search the codebase using the `repo_index` tool:

```text
Please use repo_index to find all places that call NewClient
```

`repo_index` returns summaries of related symbols rather than full file contents, helping the agent understand the codebase structure quickly.

### 12.4 Auto-Injection

When `auto_inject: true`, Lcoder does the following on each user turn:

1. Takes the first sentence of the user message as the query.
2. Searches the code index for relevant symbols.
3. Injects the results into the current context.

Example configuration:

```yaml
code_index:
  enabled: true
  watch: true
  auto_inject: true
  max_tokens: 8000
```

### 12.5 Debugging the Code Index

```bash
# Run the code-index evaluation CLI
go run ./cmd/codeindex-eval -root=. -queries="Update,Search"

# Run related tests
go test ./pkg/codeindex/...
```

---

## 13. Observability

Lcoder automatically records events during a session for later analysis of cost, performance, and behavior.

### 13.1 Data Storage Location

By default, observability data is written to:

```
~/.lcoder/observability/sessions/<session-id>.jsonl
```

Each record is a JSON object containing the event type, timestamp, and related metrics.

### 13.2 View Session Stats

```bash
./lcoder stats <session-id>
```

Example output:

```
turns: 12
input tokens: 34567
output tokens: 8901
total cost: $0.023456
```

### 13.3 View a Readable Trace

```bash
./lcoder trace <session-id>
```

Prints each turn, tool call, and event in a human-readable format.

### 13.4 Export Data

```bash
# HTML report (default)
./lcoder export <session-id>

# SQLite database
./lcoder export <session-id> --format sqlite -o report.db

# Markdown report
./lcoder export <session-id> --format markdown -o report.md

# Prometheus metrics text
./lcoder export <session-id> --format prometheus -o metrics.txt
```

### 13.5 Prometheus Metrics Endpoint

```bash
# Start on the default port :9090
./lcoder metrics

# Specify a port
./lcoder metrics 9091
```

Exposed metrics include:

- LLM call count, input/output/total tokens, cache tokens, cost.
- Tool execution count, duration, error count.
- Turn duration.
- Total session duration.

### 13.6 Configuring Observability

Observability configuration is in `~/.lcoder/observability.yaml` (see the `configs/observability.yaml` example). You can configure sampling, audit logging, context snapshots, etc.

---

## 14. Troubleshooting

### 14.1 Model Does Not Declare Tools Capability

```
warning: model "xxx" does not declare the "tools" capability; tool calls may fail
```

This means the model catalog has no capability information for the model, or the model does not support tool calling. Suggestions:

- Use a model that supports tool calling, such as `gpt-4o`, `claude-3-5-sonnet`, or `deepseek-chat`.
- Manually declare the model's `capabilities` in `models.yaml`.

### 14.2 Context Window Fallback Warning

```
warning: could not discover context window for model "xxx", falling back to default 128000
```

Lcoder could not obtain the window size from the model catalog and is using a default. Suggestions:

- Check that `provider` is configured correctly.
- Manually declare `context_window` in `models.yaml`.

### 14.3 MCP Server Connection Failure

- Ensure the command is installed correctly (stdio mode).
- Check that the URL and port are reachable (sse / streamable-http mode).
- Verify authentication headers.
- Use `/mcp` in the TUI to see detailed errors.

### 14.4 Permission Denied

If a tool is consistently blocked:

- Check `permissions.rules` in `~/.lcoder/config.yaml`.
- Check project-level `<repo>/.lcoder/permissions.yaml`.
- Check global `~/.lcoder/permissions/global.yaml`.
- Remember that more specific glob rules override general ones.

### 14.5 Configuration Validation Failure

```
invalid config: ...
```

`cfg.Validate()` checks the configuration at startup. Common errors:

- Missing required fields.
- Unknown provider.
- Invalid rule syntax.

### 14.6 TUI Display Issues

- Ensure your terminal supports 256 colors or true color.
- Try setting `TERM=xterm-256color`.
- On Windows, use Windows Terminal or a recent PowerShell version.

### 14.7 Session Not Restored Correctly

- Confirm that the corresponding file exists under `~/.lcoder/sessions/`.
- Check that you are using the correct working directory (project hash depends on it).
- If checkpoint restoration fails, Lcoder falls back to using only session messages.

---

## 15. Configuration Field Reference

This section explains every field in `configs/lcoder.yaml`.

### 15.1 Top-Level Fields

```yaml
provider: openai
model: gpt-4o-mini
# thinking: medium
# models_source: https://models.dev/api.json
```

| Field | Type | Description |
|---|---|---|
| `provider` | string | Default LLM provider. |
| `model` | string | Default model ID. |
| `thinking` | string | Thinking mode: `off` / `on` / a level declared by the model (e.g. `low`/`medium`/`high`). When unset, no thinking field is sent; undeclared levels fall back to `on` with a warning. |
| `models_source` | string | Custom models.dev-style model catalog URL (e.g. an intranet registry). The `LCODER_MODELS_SOURCE` environment variable takes precedence. |

### 15.2 TUI Configuration

```yaml
tui:
  theme: dark   # dark or light
```

### 15.3 Provider Connection Layer

```yaml
providers:
  moonshot:
    base_url: "https://api.moonshot.cn/v1"
    api_key: "{env:MOONSHOT_API_KEY}"
  myrelay:
    route: openai
    base_url: "https://api.relay.com/v1"
    api_key: "{env:RELAY_KEY}"
    headers:
      X-Title: lcoder
```

| Field | Type | Description |
|---|---|---|
| `base_url` | string | Custom API base URL. |
| `api_key` | string | API key; supports `{env:VAR}` syntax. |
| `route` | string | Protocol route, e.g. `openai` for OpenAI-compatible endpoints. |
| `headers` | map | Custom HTTP headers. |

### 15.4 Context Management

```yaml
context:
  # max_tokens: 128000
  # target_tokens: 120000
  # reserve_output: 8192
  static_ratio: 60
  min_recent: 10
  keep_recent_tokens: 20000
  compact_threshold: 0.9
  cache_hint_policy: default
```

| Field | Type | Description |
|---|---|---|
| `max_tokens` | int | Force-override the context window size. |
| `target_tokens` | int | Target token usage. |
| `reserve_output` | int | Tokens reserved for model output. |
| `static_ratio` | int | Target percentage for static/stable blocks. |
| `min_recent` | int | Minimum number of recent messages to keep during compaction. |
| `keep_recent_tokens` | int | Token budget for the tail kept during compaction. |
| `compact_threshold` | float | Trigger compaction when usage reaches this ratio of `target_tokens`. |
| `cache_hint_policy` | string | Cache breakpoint policy: `default`, `aggressive`, or `none`. |

### 15.5 Memory

```yaml
memory:
  enabled: true
  dynamic_recall: true
  recall_max_tokens: 1024
  recall_min_score: 0.1
```

| Field | Type | Description |
|---|---|---|
| `enabled` | bool | Enable persistent memory. |
| `dynamic_recall` | bool | Recall memory each turn based on relevance. |
| `recall_max_tokens` | int | Token budget for recalled memory per turn. |
| `recall_min_score` | float | Minimum relevance score (0..1) for a memory entry to be recalled. |

### 15.6 Code Index

See Chapter 12.

### 15.7 Permissions

See Chapter 10.

### 15.8 HTTP Tools and MCP Servers

See Chapter 11.

### 15.9 Hooks

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

| Field | Type | Description |
|---|---|---|
| `audit` | map | Enable audit logging. |
| `sensitive_file_check` | map | Check sensitive file access. |
| `bash_denylist` | map | Bash command denylist. |

### 15.10 Packages and Extensions

```yaml
packages:
  - name: acme-modes
    path: ~/.lcoder/packages/acme-modes
extensions:
  - name: acme-tools
    source: github.com/acme/lcoder-ext-tools
    config:
      api_key: ${ACME_API_KEY}
```

| Field | Type | Description |
|---|---|---|
| `packages` | list | Mode/skill packages to load. |
| `extensions` | list | Go extensions to install. |

---

## 16. Appendix: Environment Variables and File Paths

### 16.1 Common Environment Variables

| Variable | Description |
|---|---|
| `OPENAI_API_KEY` | OpenAI API key. |
| `ANTHROPIC_API_KEY` | Anthropic API key. |
| `DEEPSEEK_API_KEY` | DeepSeek API key. |
| `MOONSHOT_API_KEY` | Moonshot (Kimi) API key. |
| `DASHSCOPE_API_KEY` | DashScope (Qwen) API key. |

### 16.2 File Path Quick Reference

| Path | Description |
|---|---|
| `~/.lcoder/config.yaml` | Main configuration file. |
| `~/.lcoder/credentials.yaml` | API key credentials file (permissions 0600). |
| `~/.lcoder/observability.yaml` | Observability configuration file. |
| `~/.lcoder/permissions/global.yaml` | Global permission rules. |
| `~/.lcoder/sessions/<project-hash>/<session-id>.jsonl` | Session storage. |
| `~/.lcoder/observability/sessions/<session-id>.jsonl` | Observability data. |
| `~/.lcoder/skills/<name>/SKILL.md` | Global skills. |
| `~/.lcoder/modes/` | Global modes. |
| `~/.lcoder/memory/{MEMORY,USER}.md` | Global memory files. |
| `~/.lcoder/packages/` | Installed packages. |
| `~/.lcoder/extensions/` | Installed Go extension source files. |
| `<repo>/AGENTS.md` | Project-level agent instructions (searched upward to the git root). |
| `<repo>/CLAUDE.md` | Project-level Claude Code principles (searched upward to the git root). |
| `<repo>/LCODER.md` | Project-level Lcoder notes (searched upward to the git root). |
| `<repo>/.lcoder/skills/<name>/SKILL.md` | Project-level skills. |
| `<repo>/.lcoder/modes/` | Project-level modes. |
| `<repo>/.lcoder/memory/{MEMORY,USER}.md` | Project-level memory files. |
| `<repo>/.lcoder/permissions.yaml` | Project-level permission rules. |

### 16.3 Common Commands Quick Reference

```bash
# Build
go build -o lcoder ./cmd/lcoder

# Run
./lcoder -p "prompt"
./lcoder
./lcoder -c

# Manage
./lcoder models
./lcoder skills
./lcoder sessions
./lcoder modes

# Observability
./lcoder stats <id>
./lcoder trace <id>
./lcoder export <id> --format html
./lcoder metrics 9090

# Extensions
./lcoder install SOURCE --name NAME
./lcoder uninstall NAME
./lcoder list-extensions
./lcoder update NAME
```

---

> This manual is based on the current Lcoder implementation. For feature changes, please refer to the source code and `README_EN.md`.
