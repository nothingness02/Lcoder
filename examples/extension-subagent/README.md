# Subagent Extension Example

This example provides a Go plugin extension that adds a `subagent` tool to Lcoder.
The tool delegates work to other Lcoder agents defined in the current project.

## Build

Go plugins require Linux, macOS, or FreeBSD and `CGO_ENABLED=1`:

```bash
cd examples/extension-subagent
go build -buildmode=plugin -o subagent.so .
```

## Configure

Add the built plugin to `~/.lcoder/config.yaml`:

```yaml
tool_extensions:
  - name: subagent
    type: go-plugin
    path: /absolute/path/to/examples/extension-subagent/subagent.so
```

## Agent definition example

Place an agent definition in your project's `.lcoder/agents/` directory (or in `~/.lcoder/agents/`). Agent files are Markdown with YAML frontmatter, e.g. `.lcoder/agents/researcher.md`:

```markdown
---
name: researcher
description: A focused research agent
model: claude-sonnet-4-20250514
provider: anthropic
mode: code
timeout: 120
---
You are a research assistant. Answer the user's question concisely.
```

## Usage

### Single subagent

```json
{
  "agent": "researcher",
  "task": "Summarize the Go plugin documentation."
}
```

### Parallel subagents

```json
{
  "tasks": [
    {"agent": "researcher", "task": "Summarize Go plugins."},
    {"agent": "researcher", "task": "Summarize Go modules."}
  ]
}
```

Results are returned as:

```
[0] <result from first task>

[1] <result from second task>
```

If a task errors, its slot is formatted as `[i] ERROR: <message>`.

### Chain subagents

```json
{
  "chain": [
    {"agent": "researcher", "task": "Find the latest Go release."},
    {"agent": "writer", "task": "Draft a blog post about {previous}."}
  ]
}
```

The literal string `{previous}` in a chained task is replaced with the previous step's output.

## Notes

- Subagents currently invoke the `lcoder` CLI and leave sessions under `~/.lcoder/sessions/` and automatic checkpoints under `~/.lcoder/checkpoints/`. There is no ephemeral mode yet, so disk usage can grow with heavy use.
- The plugin uses the host project's working directory as the project root when discovering agents.
