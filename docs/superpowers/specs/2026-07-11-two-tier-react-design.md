# Two-tier ReAct / Model-Driven Mode Switching Design

> **Goal:** Let the model switch between plan and execution modes autonomously via a `switch_mode` meta-tool, so Lcoder can do "plan first, then execute" like Claude Code.

**Architecture:** Add a `switch_mode` meta-tool that is intercepted by the agent executor (similar to `tool_search`). When the model calls it, the executor validates the target mode and updates `Agent.cfg.Mode`. On the next turn, the existing `applyMode()` logic reloads the system prompt, tool allowlist, and execution mode for the new mode. No separate planner runtime is introduced; the same agent loop simply changes mode context between turns.

**Tech Stack:** Go, existing `pkg/agent` executor/mode machinery, YAML mode configs.

---

## Background

Lcoder already has:

- `ModeManager` loading YAML mode configs from `configs/agents/*.yaml`.
- `Agent.applyMode()` selecting system prompt, tool list, model override, and execution mode per turn.
- `executor` handling meta-tools such as `tool_search` locally before registry dispatch.
- Checkpoints already persist `AgentSnapshot.Mode`.

The missing piece is a runtime mechanism for the model itself to request a mode change.

---

## Design

### 1. Meta-tool: `switch_mode`

`switch_mode` is exposed to the model as a normal tool but resolved locally by the executor.

**Definition:**

```go
models.ToolDefinition{
    Name:        "switch_mode",
    Description: "Switch the agent to a different mode for subsequent turns. Use this to move from planning to implementation or back.",
    Parameters: map[string]any{
        "type": "object",
        "properties": map[string]any{
            "mode": map[string]any{
                "type":        "string",
                "description": "Target mode name, e.g. code or plan",
            },
        },
        "required": []string{"mode"},
    },
}
```

**Execution behavior (in `pkg/agent/executor.go`):**

- Intercept `call.Name == "switch_mode"` before registry dispatch, like `tool_search`.
- Read `args["mode"]` string.
- Validate against `cfg.ModeManager.Get(mode)`. If unknown, return an error tool result and keep the current mode.
- On success, update `e.cfg.Mode = target` and return a confirmation text result.
- The switch takes effect on the **next** turn via `applyMode()`.

### 2. Tool list wiring

- `executor.baseToolDefinitions()` appends the `switch_mode` definition to the list returned to the model.
- Because it is appended before `applyMode()` filters tools, mode-level `allowed_tools` / `denied_tools` control visibility normally.
- `plan` mode adds `switch_mode` to `allowed_tools`.
- `code` mode leaves it allowed (no `denied_tools` entry) so the model can optionally switch back for re-planning.

### 3. Mode prompt updates

`configs/agents/plan.yaml`:

```yaml
name: plan
description: Architecture and planning mode
system_prompt: |
  You are in planning mode. Do not write or edit files unless explicitly asked.
  Analyze the request, break it into steps, and present a clear plan.
  Ask clarifying questions when requirements are ambiguous.
  You may read files and explore the codebase to understand context.
  When your plan is ready, call switch_mode with mode="code" to begin implementation.
allowed_tools:
  - read
  - ls
  - grep
  - find
  - switch_mode
denied_tools:
  - write
  - edit
  - bash
execution_mode: sequential
```

`configs/agents/code.yaml`:

```yaml
name: code
description: Default coding and implementation mode
system_prompt: |
  You are in coding mode. Implement the plan by editing files, running tests, and verifying changes.
  Prefer precise edits and explain your reasoning briefly.
  Always run relevant tests after making changes.
  If you need to significantly revise the plan, call switch_mode with mode="plan" to return to planning.
allowed_tools: []
denied_tools: []
execution_mode: parallel
```

### 4. Data flow

1. User sends a planning-style request (or `--mode plan`).
2. `ModeManager.Detect` selects `plan` (or `--mode plan` pins it).
3. Plan turn: model reads files, emits plan, calls `switch_mode{"mode":"code"}`.
4. Executor validates and sets `cfg.Mode = "code"`.
5. Turn ends; checkpoint is saved with `Mode: "code"`.
6. Next turn: `applyMode()` loads code system prompt and full tool set.
7. Model executes edits/tests in code mode.

### 5. Error handling and edge cases

- **Unknown target mode:** Return error result; current mode unchanged.
- **Missing `mode` argument:** Return validation error result.
- **Mixed tool calls in the same turn:** Other tools execute under the current mode's rules. Plan mode denies writes, so mixing is prevented by the allowlist.
- **Mode change mid-parallel execution:** The change is serialized through `cfg.Mode` and observed only at the next `applyMode()` call, so no race exists.
- **Restore from checkpoint:** `AgentSnapshot.Mode` already persists the active mode; `Restore` sets `cfg.Mode` before the next turn.

### 6. Testing strategy

- **Unit tests in `pkg/agent/executor_test.go`:**
  - `TestSwitchModeValid`: model calls `switch_mode{"mode":"code"}` while in plan mode; assert `cfg.Mode == "code"` and result is non-error.
  - `TestSwitchModeUnknown`: unknown mode returns error and does not change `cfg.Mode`.
  - `TestSwitchModeDefinitionPresent`: `baseToolDefinitions()` includes `switch_mode`.
  - `TestApplyModeFiltersSwitchMode`: when plan mode allows `switch_mode` it is present; when denied it is absent.
- **Integration test (if feasible with `llmtest`):**
  - Scripted assistant message calls `switch_mode`; assert the following turn's tool list contains `edit`.

### 7. Files to modify

- `pkg/agent/executor.go` — add `switch_mode` handling and definition.
- `configs/agents/plan.yaml` — update prompt and allow `switch_mode`.
- `configs/agents/code.yaml` — update prompt.
- `pkg/agent/executor_test.go` — add unit tests.

### 8. Out of scope

- A dedicated planner LLM or separate planning runtime.
- Automatic mode detection of when to switch (the model decides).
- UI-mode indicators or TUI animations for mode switches.

---

## Decision log

- **Why a meta-tool instead of a content tag?** A tool uses the existing tool-calling schema, is observable in events/audit logs, and is validated by argument parsing. Content tags are brittle and can leak into user-visible text.
- **Why not a separate planner process?** The existing mode machinery already supports distinct system prompts and tool allowlists; switching modes keeps the architecture minimal and checkpoint-compatible.
- **Why allow `switch_mode` in code mode?** Re-planning is a natural recovery behavior; denying it would force the user to restart the session.
