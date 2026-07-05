# Unify and Cleanup Tool Parameter Validation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The unified `tools.ValidateArgs` already runs before every tool call. Finish the work by removing duplicate manual validation from each built-in tool and providing a small typed-access helper so tools no longer write their own `float64 → int` conversions.

**Architecture:** Add `pkg/tools/args.go` with typed accessors (`String`, `Int`, `IntDefault`, `StringSlice`, etc.). Refactor `pkg/tools/builtin/read.go`, `write.go`, `edit.go`, `bash.go` to rely on `ValidateArgs` for presence/type checks and use `args.go` helpers for value extraction. Delete the redundant `missing X` checks and inline conversions.

**Tech Stack:** Go 1.25, existing `pkg/tools/validate.go`, `pkg/tools/builtin`.

---

## File Structure

- **Create:** `pkg/tools/args.go`
- **Create:** `pkg/tools/args_test.go`
- **Modify:** `pkg/tools/builtin/read.go`
- **Modify:** `pkg/tools/builtin/write.go`
- **Modify:** `pkg/tools/builtin/edit.go`
- **Modify:** `pkg/tools/builtin/bash.go`
- **Modify:** `pkg/tools/builtin/ls.go` (if it has manual validation)
- **Modify:** `pkg/tools/builtin/grep.go` (if it has manual validation)

---

## Task 1: Typed Argument Access Helpers

**Files:**
- Create: `pkg/tools/args.go`
- Create: `pkg/tools/args_test.go`

- [ ] **Step 1: Write the failing test**

Create `pkg/tools/args_test.go`:

```go
package tools

import "testing"

func TestArgs_Int(t *testing.T) {
	args := map[string]any{"timeout": float64(120)}
	v, ok := Int(args, "timeout")
	if !ok || v != 120 {
		t.Fatalf("expected 120, got %d ok=%v", v, ok)
	}
	_, ok = Int(args, "missing")
	if ok {
		t.Fatal("expected missing field to report not ok")
	}
}

func TestArgs_IntDefault(t *testing.T) {
	args := map[string]any{}
	if v := IntDefault(args, "timeout", 60); v != 60 {
		t.Fatalf("expected default 60, got %d", v)
	}
}

func TestArgs_String(t *testing.T) {
	args := map[string]any{"path": "main.go"}
	if v, ok := String(args, "path"); !ok || v != "main.go" {
		t.Fatalf("expected main.go, got %q ok=%v", v, ok)
	}
}
```

Run:
```bash
go test ./pkg/tools/ -run TestArgs -v
```
Expected: FAIL — helpers do not exist.

- [ ] **Step 2: Implement helpers**

Create `pkg/tools/args.go`:

```go
package tools

import "fmt"

// String returns a string argument.
func String(args map[string]any, key string) (string, bool) {
	v, ok := args[key].(string)
	return v, ok
}

// StringOr returns a string argument or the given default.
func StringOr(args map[string]any, key, def string) string {
	if v, ok := String(args, key); ok {
		return v
	}
	return def
}

// Int returns an integer argument, accepting float64/int/int64.
func Int(args map[string]any, key string) (int, bool) {
	switch v := args[key].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	}
	return 0, false
}

// IntDefault returns an integer argument or the given default.
func IntDefault(args map[string]any, key string, def int) int {
	if v, ok := Int(args, key); ok {
		return v
	}
	return def
}

// Bool returns a boolean argument.
func Bool(args map[string]any, key string) (bool, bool) {
	v, ok := args[key].(bool)
	return v, ok
}

// Slice returns a []any argument.
func Slice(args map[string]any, key string) ([]any, bool) {
	v, ok := args[key].([]any)
	return v, ok
}

// Object returns a map[string]any argument.
func Object(args map[string]any, key string) (map[string]any, bool) {
	v, ok := args[key].(map[string]any)
	return v, ok
}

// RequiredString returns a string argument or a formatted error.
func RequiredString(args map[string]any, key string) (string, error) {
	if v, ok := String(args, key); ok && v != "" {
		return v, nil
	}
	return "", fmt.Errorf("missing required argument %q", key)
}
```

Run:
```bash
go test ./pkg/tools/ -run TestArgs -v
```
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/tools/args.go pkg/tools/args_test.go
git commit -m "feat(tools): typed argument accessors

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 2: Refactor Built-in Tools

**Files:**
- Modify: `pkg/tools/builtin/read.go`
- Modify: `pkg/tools/builtin/write.go`
- Modify: `pkg/tools/builtin/edit.go`
- Modify: `pkg/tools/builtin/bash.go`

- [ ] **Step 1: Refactor read.go**

Modify `pkg/tools/builtin/read.go` `Execute`:

```go
func (r *Read) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	path, err := tools.RequiredString(args, "path")
	if err != nil {
		return models.ToolExecutionResult{}, err
	}
	path, err = resolveAndCheck(r.cwd, r.sb, path, sandbox.FSRead)
	// ... rest unchanged ...
	offset := tools.IntDefault(args, "offset", 1)
	limit := tools.IntDefault(args, "limit", 0)
	// ... rest unchanged ...
}
```

Remove the old `path, ok := args["path"].(string)` block.

- [ ] **Step 2: Refactor write.go**

```go
func (w *Write) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	path, err := tools.RequiredString(args, "path")
	if err != nil {
		return models.ToolExecutionResult{}, err
	}
	content, err := tools.RequiredString(args, "content")
	if err != nil {
		return models.ToolExecutionResult{}, err
	}
	// ... rest unchanged ...
}
```

- [ ] **Step 3: Refactor edit.go**

```go
func (e *Edit) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	path, err := tools.RequiredString(args, "path")
	if err != nil {
		return models.ToolExecutionResult{}, err
	}
	editsRaw, ok := tools.Slice(args, "edits")
	if !ok || len(editsRaw) == 0 {
		return models.ToolExecutionResult{}, fmt.Errorf("missing edits")
	}
	// ... rest unchanged ...
}
```

- [ ] **Step 4: Refactor bash.go**

```go
func (b *Bash) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	command, err := tools.RequiredString(args, "command")
	if err != nil {
		return models.ToolExecutionResult{}, err
	}
	timeout := tools.IntDefault(args, "timeout", 60)
	// ... rest unchanged, remove old float64 conversion ...
}
```

- [ ] **Step 5: Run builtin tests**

```bash
go test ./pkg/tools/builtin/... -count=1
```
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/tools/builtin/read.go pkg/tools/builtin/write.go pkg/tools/builtin/edit.go pkg/tools/builtin/bash.go
git commit -m "refactor(tools): built-in tools use typed arg helpers

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 3: Full Verification

- [ ] **Step 1: Run affected packages**

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
   - Typed accessors: Task 1
   - Built-in tool cleanup: Task 2

2. **Placeholder scan:** No TBD/TODO.

3. **Type consistency:** `tools.Int` accepts `float64/int/int64` and returns `int`.
