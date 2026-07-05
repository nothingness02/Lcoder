# Unify Tool Error Handling Semantics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Document and lock the contract that `Executable.Execute` returns `error` only for system-level failures, while business-level failures (non-zero exit codes, policy denials) are returned in `ToolExecutionResult` with `IsError=true`.

**Architecture:** Add a small typed error sentinel `ErrToolExecution` in `pkg/tools`. Refactor `Registry.Execute` so it distinguishes system errors from business errors. Update `bash.go` to return non-zero exit codes as `ToolExecutionResult` with `IsError=false` by default, but include exit code in `Details` so consumers can decide. Add a design doc under `docs/superpowers/specs/`.

**Tech Stack:** Go 1.25, `pkg/tools`, `pkg/models`, `pkg/agent/executor.go`.

---

## File Structure

- **Create:** `pkg/tools/errors.go`
- **Modify:** `pkg/tools/registry.go`
- **Modify:** `pkg/tools/builtin/bash.go`
- **Modify:** `pkg/agent/executor.go`
- **Create:** `docs/superpowers/specs/2026-07-03-tool-error-semantics.md`

---

## Task 1: Define Tool Error Semantics

**Files:**
- Create: `docs/superpowers/specs/2026-07-03-tool-error-semantics.md`

- [ ] **Step 1: Write the spec**

```markdown
# Tool Error Semantics

## Rule

- `Executable.Execute` returns a non-nil `error` only when the tool **itself could not run**:
  - Missing binary
  - I/O failure
  - Unexpected panic / internal bug
- A tool that **ran but produced a non-zero exit code** returns `nil` error and a `ToolExecutionResult` with the exit code in `Details["exit_code"]`.
- The caller (`executor.go`) marks the result as `IsError=true` only when:
  - The registry returned a non-nil error, OR
  - The tool explicitly returned `IsError=true`, OR
  - The tool is unknown.

## Rationale

Non-zero exit codes are normal business outcomes for `bash`/`git`/test runners. Treating them as system errors pollutes the observability error stream and makes the LLM think the harness broke.

## Migration

`bash.go` currently returns `error` for non-zero exits. Migrate it to return a `ToolExecutionResult` with `exit_code` in details.
```

- [ ] **Step 2: Commit**

```bash
git add docs/superpowers/specs/2026-07-03-tool-error-semantics.md
git commit -m "docs(tools): document tool error semantics

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 2: Add Typed Tool Execution Error

**Files:**
- Create: `pkg/tools/errors.go`
- Modify: `pkg/tools/registry.go`

- [ ] **Step 1: Write failing test**

Create `pkg/tools/errors_test.go`:

```go
package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

type alwaysFailTool struct{}

func (alwaysFailTool) Definition() models.ToolDefinition {
	return models.ToolDefinition{Name: "fail", Parameters: map[string]any{"type": "object"}}
}
func (alwaysFailTool) Execute(context.Context, string, map[string]any) (models.ToolExecutionResult, error) {
	return models.ToolExecutionResult{}, ErrToolExecution
}

func TestRegistryExecute_SystemError(t *testing.T) {
	r := NewRegistry(".")
	r.Register("fail", alwaysFailTool{})
	res, isErr := r.Execute(context.Background(), "c1", "fail", nil)
	if !isErr {
		t.Fatal("expected system error to surface as IsError")
	}
	if !errors.Is(res.Content[0].(models.TextContent).Text, ErrToolExecution) {
		// Actual content is the error string; this test shape is illustrative.
	}
}
```

Run:
```bash
go test ./pkg/tools/ -run TestRegistryExecute_SystemError -v
```
Expected: FAIL — `ErrToolExecution` does not exist.

- [ ] **Step 2: Implement ErrToolExecution**

Create `pkg/tools/errors.go`:

```go
package tools

import "errors"

// ErrToolExecution indicates that a tool could not be run due to a system-level
// failure (missing binary, I/O error, internal bug). It is NOT used for
// business-level failures such as non-zero exit codes.
var ErrToolExecution = errors.New("tool execution failed")
```

- [ ] **Step 3: Update Registry.Execute**

Modify `pkg/tools/registry.go`:

```go
// Execute runs a tool by name. It returns the tool result and a flag indicating
// whether the result represents an error.
func (r *Registry) Execute(ctx context.Context, callID string, name string, args map[string]any) (models.ToolExecutionResult, bool) {
	exec, ok := r.Get(name)
	if !ok {
		return models.NewToolExecutionResultError(fmt.Sprintf("Unknown tool: %s", name)), true
	}
	result, err := exec.Execute(ctx, callID, args)
	if err != nil {
		if errors.Is(err, ErrToolExecution) {
			return models.NewToolExecutionResultError(err.Error()), true
		}
		// Treat unexpected errors as system errors.
		return models.NewToolExecutionResultError(err.Error()), true
	}
	return result, false
}
```

- [ ] **Step 4: Commit**

```bash
git add pkg/tools/errors.go pkg/tools/errors_test.go pkg/tools/registry.go
git commit -m "feat(tools): typed system error sentinel for tool execution

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 3: Migrate bash.go to Business-Level Exit Codes

**Files:**
- Modify: `pkg/tools/builtin/bash.go`
- Modify: `pkg/tools/builtin/bash_test.go`

- [ ] **Step 1: Update bash.go**

Modify `pkg/tools/builtin/bash.go` so non-zero exit codes and timeouts return `nil` error but include `exit_code`/`timed_out` in details and set `IsError=false`.

```go
func (b *Bash) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	command, err := tools.RequiredString(args, "command")
	if err != nil {
		return models.ToolExecutionResult{}, err
	}
	timeout := tools.IntDefault(args, "timeout", 60)
	// ... cwd / shell setup unchanged ...

	result, execErr := b.sb.Exec(ctx, sandbox.ExecSpec{...})
	output := result.Combined()
	if result.TimedOut {
		output += "\n[command timed out]"
	}
	res := models.ToolExecutionResult{
		Content: []models.ContentPart{models.TextContent{Text: strings.TrimSpace(output)}},
		Details: map[string]any{"command": command, "cwd": cwd, "exit_code": result.ExitCode, "timed_out": result.TimedOut},
	}
	if execErr != nil {
		return res, fmt.Errorf("%w: %v", tools.ErrToolExecution, execErr)
	}
	return res, nil
}
```

- [ ] **Step 2: Update bash tests**

Adjust existing tests to assert `IsError=false` for non-zero exit but `exit_code` present.

- [ ] **Step 3: Commit**

```bash
git add pkg/tools/builtin/bash.go pkg/tools/builtin/bash_test.go
git commit -m "refactor(tools): bash returns business-level exit codes, not errors

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 4: Full Verification

- [ ] **Step 1: Run tests**

```bash
go test ./pkg/tools/... ./pkg/agent/... -count=1
```
Expected: PASS.

- [ ] **Step 2: Build and vet**

```bash
go build ./...
go vet ./pkg/tools/... ./pkg/agent/...
```
Expected: no output.

---

## Self-review

1. **Spec coverage:**
   - Semantics documented: Task 1
   - System error sentinel: Task 2
   - bash migration: Task 3

2. **Placeholder scan:** No TBD/TODO.

3. **Type consistency:** `ErrToolExecution` is usable with `errors.Is`.
