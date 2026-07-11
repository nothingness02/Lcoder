# Two-tier ReAct / Model-Driven Mode Switching Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `switch_mode` meta-tool so the model can switch from plan mode to code mode (and back) between turns, enabling plan-then-execute behavior.

**Architecture:** The executor intercepts `switch_mode` like it does `tool_search`. A successful call updates `Config.Mode`; the next turn's existing `applyMode()` applies the new system prompt and tool allowlist. Mode prompt YAMLs are updated to instruct the model when to switch.

**Tech Stack:** Go, existing `pkg/agent` executor/mode machinery, YAML mode configs.

---

## File Structure

- `pkg/agent/executor.go` — add `switch_mode` definition, handler, interception, and append it to the tool list.
- `configs/agents/plan.yaml` — update prompt and allow `switch_mode`.
- `configs/agents/code.yaml` — update prompt.
- `pkg/agent/executor_switch_test.go` — new tests for switching, unknown modes, and tool-list wiring.

---

### Task 1: Add `switch_mode` handler and definition

**Files:**
- Modify: `pkg/agent/executor.go`
- Create: `pkg/agent/executor_switch_test.go`

- [ ] **Step 1: Write the failing tests**

Create `pkg/agent/executor_switch_test.go`:

```go
package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
)

func TestHandleSwitchMode_Valid(t *testing.T) {
	bus := events.New()
	cfg := Config{Mode: "plan", ModeManager: NewModeManager()}
	e := &executor{cfg: &cfg, emitter: &eventEmitter{bus: bus}}

	call := models.ToolCallContent{
		ID:        "call_1",
		Name:      switchModeToolName,
		Arguments: map[string]any{"mode": "code"},
	}
	msg := e.handleSwitchMode(context.Background(), 0, models.AgentMessage{}, call)

	if cfg.Mode != "code" {
		t.Fatalf("expected mode switched to code, got %q", cfg.Mode)
	}
	trc := msg.Content[0].(models.ToolResultContent)
	if trc.IsError {
		t.Fatalf("expected non-error result, got error")
	}
	if !strings.Contains(trc.Text(), "code") {
		t.Fatalf("expected result to mention code, got %q", trc.Text())
	}
}

func TestHandleSwitchMode_Unknown(t *testing.T) {
	bus := events.New()
	cfg := Config{Mode: "plan", ModeManager: NewModeManager()}
	e := &executor{cfg: &cfg, emitter: &eventEmitter{bus: bus}}

	call := models.ToolCallContent{
		ID:        "call_2",
		Name:      switchModeToolName,
		Arguments: map[string]any{"mode": "nonexistent"},
	}
	msg := e.handleSwitchMode(context.Background(), 0, models.AgentMessage{}, call)

	if cfg.Mode != "plan" {
		t.Fatalf("expected mode unchanged, got %q", cfg.Mode)
	}
	trc := msg.Content[0].(models.ToolResultContent)
	if !trc.IsError {
		t.Fatal("expected error result for unknown mode")
	}
}

func TestHandleSwitchMode_MissingArg(t *testing.T) {
	bus := events.New()
	cfg := Config{Mode: "plan", ModeManager: NewModeManager()}
	e := &executor{cfg: &cfg, emitter: &eventEmitter{bus: bus}}

	call := models.ToolCallContent{
		ID:        "call_3",
		Name:      switchModeToolName,
		Arguments: map[string]any{},
	}
	msg := e.handleSwitchMode(context.Background(), 0, models.AgentMessage{}, call)

	if cfg.Mode != "plan" {
		t.Fatalf("expected mode unchanged, got %q", cfg.Mode)
	}
	trc := msg.Content[0].(models.ToolResultContent)
	if !trc.IsError {
		t.Fatal("expected error result for missing mode argument")
	}
}
```

- [ ] **Step 2: Run the tests and verify they fail**

Run:

```bash
cd D:/code_practise/project/lab_pj/Lcoder
go test ./pkg/agent -run TestHandleSwitchMode -v
```

Expected: FAIL — `switchModeToolName` and `handleSwitchMode` undefined.

- [ ] **Step 3: Add the definition and handler to executor.go**

In `pkg/agent/executor.go`, near the `tool_search` handling, add:

```go
const switchModeToolName = "switch_mode"

func switchModeDefinition() models.ToolDefinition {
	return models.ToolDefinition{
		Name:        switchModeToolName,
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
}

func (e *executor) handleSwitchMode(ctx context.Context, turn int, assistantMsg models.AgentMessage, call models.ToolCallContent) models.AgentMessage {
	e.emitter.emit(ctx, events.ToolExecutionStartEvent{
		Base:       events.Base{Type: events.ToolExecutionStart, Turn: turn},
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Args:       call.Arguments,
	})

	args := call.Arguments
	if args == nil {
		args = make(map[string]any)
	}

	target, _ := args["mode"].(string)
	var result models.ToolExecutionResult
	isError := false
	if target == "" {
		result = models.NewToolExecutionResultError("missing mode argument")
		isError = true
	} else if e.cfg.ModeManager == nil || e.cfg.ModeManager.Get(target).Name != target {
		result = models.NewToolExecutionResultError("unknown mode: " + target)
		isError = true
	} else {
		e.cfg.Mode = target
		result = models.NewToolExecutionResultText("Switched to " + target + " mode")
	}

	e.emitter.emit(ctx, events.ToolExecutionEndEvent{
		Base:       events.Base{Type: events.ToolExecutionEnd, Turn: turn},
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Result:     result,
		IsError:    isError,
	})

	return e.makeToolResultMessage(call, result, isError)
}
```

`models` and `events` are already imported in `executor.go`.

- [ ] **Step 4: Run the tests and verify they pass**

Run:

```bash
cd D:/code_practise/project/lab_pj/Lcoder
go test ./pkg/agent -run TestHandleSwitchMode -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd D:/code_practise/project/lab_pj/Lcoder
git add pkg/agent/executor.go pkg/agent/executor_switch_test.go
git commit -m "feat(agent): add switch_mode meta-tool handler and definition

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 2: Wire `switch_mode` into execution and tool list

**Files:**
- Modify: `pkg/agent/executor.go`
- Modify: `pkg/agent/executor_switch_test.go`

- [ ] **Step 1: Add the interception and tool-list wiring tests**

Append to `pkg/agent/executor_switch_test.go`:

```go
func TestSwitchMode_InterceptsExecution(t *testing.T) {
	bus := events.New()
	cfg := Config{Mode: "plan", ModeManager: NewModeManager()}
	e := &executor{cfg: &cfg, registry: tools.NewRegistry("."), emitter: &eventEmitter{bus: bus}}

	call := models.ToolCallContent{
		ID:        "call_4",
		Name:      switchModeToolName,
		Arguments: map[string]any{"mode": "code"},
	}
	msg := e.executeOneToolCall(context.Background(), 0, models.AgentMessage{}, call)

	if cfg.Mode != "code" {
		t.Fatalf("expected mode switched to code, got %q", cfg.Mode)
	}
	trc := msg.Content[0].(models.ToolResultContent)
	if trc.IsError {
		t.Fatalf("expected non-error result, got error")
	}
}

func TestBaseToolDefinitions_IncludesSwitchMode(t *testing.T) {
	cfg := Config{Mode: "code", ModeManager: NewModeManager()}
	e := &executor{cfg: &cfg, registry: tools.NewRegistry("."), activeDeferred: make(map[string]bool)}

	defs := e.baseToolDefinitions()
	found := false
	for _, d := range defs {
		if d.Name == switchModeToolName {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected baseToolDefinitions to include switch_mode")
	}
}
```

Add import for `github.com/lcoder/lcoder/pkg/tools` to the test file.

- [ ] **Step 2: Run the tests and verify they fail**

```bash
cd D:/code_practise/project/lab_pj/Lcoder
go test ./pkg/agent -run TestSwitchMode_InterceptsExecution|TestBaseToolDefinitions_IncludesSwitchMode -v
```

Expected: FAIL — `switch_mode` is not intercepted and not in `baseToolDefinitions`.

- [ ] **Step 3: Wire interception and tool list in executor.go**

In `executeOneToolCall`, immediately after the `tool_search` check, add:

```go
	if call.Name == switchModeToolName {
		return e.handleSwitchMode(ctx, turn, assistantMsg, call)
	}
```

Update `baseToolDefinitions` so the switch_mode definition is always appended. Change the end of the function from:

```go
	return append(active, deferred...)
}
```

to:

```go
	return append(append(active, deferred...), switchModeDefinition())
}
```

and for the non-deferred branch change:

```go
	return e.registry.Definitions()
}
```

to:

```go
	return append(e.registry.Definitions(), switchModeDefinition())
}
```

- [ ] **Step 4: Run the tests and verify they pass**

```bash
cd D:/code_practise/project/lab_pj/Lcoder
go test ./pkg/agent -run TestSwitchMode -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd D:/code_practise/project/lab_pj/Lcoder
git add pkg/agent/executor.go pkg/agent/executor_switch_test.go
git commit -m "feat(agent): wire switch_mode into execution and tool list

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 3: Update mode prompts

**Files:**
- Modify: `configs/agents/plan.yaml`
- Modify: `configs/agents/code.yaml`

- [ ] **Step 1: Update plan.yaml**

Replace `configs/agents/plan.yaml` with:

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

- [ ] **Step 2: Update code.yaml**

Replace `configs/agents/code.yaml` with:

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

- [ ] **Step 3: Validate YAML loading**

Run:

```bash
cd D:/code_practise/project/lab_pj/Lcoder
go test ./pkg/agent -run TestModeManagerLoadAndGet -v
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
cd D:/code_practise/project/lab_pj/Lcoder
git add configs/agents/plan.yaml configs/agents/code.yaml
git commit -m "feat(config): instruct models to use switch_mode in plan and code modes

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 4: Full verification and cleanup

**Files:**
- All of the above

- [ ] **Step 1: Run the agent package tests**

```bash
cd D:/code_practise/project/lab_pj/Lcoder
go test ./pkg/agent -v
```

Expected: all PASS.

- [ ] **Step 2: Run the full test suite (excluding reference/Shannon)**

```bash
cd D:/code_practise/project/lab_pj/Lcoder
go test $(go list ./... | grep -v 'reference/Shannon')
```

Expected: all PASS.

- [ ] **Step 3: Run vet**

```bash
cd D:/code_practise/project/lab_pj/Lcoder
go vet $(go list ./... | grep -v 'reference/Shannon')
```

Expected: no output / PASS.

- [ ] **Step 4: Final build**

```bash
cd D:/code_practise/project/lab_pj/Lcoder
go build ./cmd/lcoder
```

Expected: builds without errors.

- [ ] **Step 5: Commit any remaining changes**

If there are no uncommitted changes, skip. Otherwise:

```bash
cd D:/code_practise/project/lab_pj/Lcoder
git add -A
git commit -m "chore: verify two-tier ReAct wiring and tests

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Spec Coverage Check

| Spec requirement | Task |
| --- | --- |
| `switch_mode` meta-tool definition | Task 1 |
| Executor intercepts and handles `switch_mode` | Task 2 |
| Mode validation and error handling | Task 1 |
| `switch_mode` appended to tool list | Task 2 |
| plan.yaml allows and instructs `switch_mode` | Task 3 |
| code.yaml mentions `switch_mode` for re-planning | Task 3 |
| Unit tests for switching, unknown modes, wiring | Task 1, Task 2 |
| Full test suite passes | Task 4 |

No placeholders or unresolved items remain.
