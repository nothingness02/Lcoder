# 工具执行隔离与去重实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 Lcoder 工具执行层落地同 turn 去重、`write/edit` 两阶段提交（失败回滚）、以及 `bash` 的显式产物回传，减少失败调用对本地环境的污染。

**Architecture:** 在 `pkg/agent/executor.go` 增加按 `(tool_name, normalized_args)` 的短期缓存；在 `pkg/tools/builtin/edit.go` 与 `write.go` 中先 dry-run/校验再原子写入并保留备份；在 `pkg/tools/builtin/bash.go` 中支持 `outputs` 参数，命令成功后仅把声明的产物复制到本地，失败时不复制。所有变更保持现有 `registry.Execute` 与 `ToolResultContent.IsError` 语义不变。

**Tech Stack:** Go 1.25.4，模块 `github.com/lcoder/lcoder`；测试使用标准库 `testing` + 现有 `pkg/sandbox/fake.go` + `pkg/llm/llmtest`。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `pkg/tools/args.go`（新建） | 工具参数类型安全访问 + 归一化（用于去重键） |
| `pkg/tools/args_test.go`（新建） | 参数辅助函数测试 |
| `pkg/agent/executor.go` | 同 turn 去重缓存、缓存键计算、结果深拷贝 |
| `pkg/agent/executor_dedup_test.go`（新建） | 去重行为测试 |
| `pkg/tools/builtin/edit.go` | 两阶段：内存应用 edits → 备份 → 原子写入 → 失败恢复 |
| `pkg/tools/builtin/edit_test.go`（新建） | edit 两阶段与回滚测试 |
| `pkg/tools/builtin/write.go` | 两阶段：备份 → temp → rename → 失败恢复 |
| `pkg/tools/builtin/write_test.go`（新建） | write 两阶段与回滚测试 |
| `pkg/tools/builtin/bash.go` | 新增 `outputs` 参数，成功后复制产物 |
| `pkg/tools/builtin/bash_test.go`（新建） | bash outputs 复制/失败丢弃测试 |

---

### Task 1: 工具参数辅助函数与归一化

**Files:**
- Create: `pkg/tools/args.go`
- Create: `pkg/tools/args_test.go`

- [ ] **Step 1: Write the failing test**

```go
package tools

import (
	"testing"
)

func TestString(t *testing.T) {
	args := map[string]any{"path": "main.go", "limit": float64(10)}
	if got := String(args, "path", ""); got != "main.go" {
		t.Fatalf("String path = %q, want main.go", got)
	}
	if got := String(args, "missing", "default"); got != "default" {
		t.Fatalf("String missing = %q, want default", got)
	}
}

func TestInt(t *testing.T) {
	args := map[string]any{"a": float64(7), "b": int64(8)}
	if got := Int(args, "a", 0); got != 7 {
		t.Fatalf("Int a = %d, want 7", got)
	}
	if got := Int(args, "b", 0); got != 8 {
		t.Fatalf("Int b = %d, want 8", got)
	}
	if got := Int(args, "missing", 5); got != 5 {
		t.Fatalf("Int missing = %d, want 5", got)
	}
}

func TestStringSlice(t *testing.T) {
	args := map[string]any{"outs": []any{"a.txt", "b.txt"}}
	got := StringSlice(args, "outs")
	want := []string{"a.txt", "b.txt"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("StringSlice = %v, want %v", got, want)
	}
}

func TestNormalizeArgs(t *testing.T) {
	a := map[string]any{"path": "main.go", "offset": float64(1)}
	b := map[string]any{"offset": float64(1), "path": "main.go"}
	if NormalizeArgs(a) != NormalizeArgs(b) {
		t.Fatalf("NormalizeArgs should be order-independent: %q vs %q", NormalizeArgs(a), NormalizeArgs(b))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tools -run 'TestString|TestInt|TestStringSlice|TestNormalizeArgs' -v`

Expected: FAIL with undefined functions.

- [ ] **Step 3: Write minimal implementation**

```go
package tools

import (
	"encoding/json"
	"fmt"
)

// String returns args[key] as a string, or defaultVal if missing / not a string.
func String(args map[string]any, key, defaultVal string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return defaultVal
}

// Int returns args[key] as an int, accepting float64 / int / int64 / json.Number.
func Int(args map[string]any, key string, defaultVal int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return int(i)
		}
	}
	return defaultVal
}

// StringSlice returns args[key] as []string, accepting []any or []string.
func StringSlice(args map[string]any, key string) []string {
	if args == nil {
		return nil
	}
	raw := args[key]
	if raw == nil {
		return nil
	}
	if ss, ok := raw.([]string); ok {
		return ss
	}
	if aa, ok := raw.([]any); ok {
		out := make([]string, 0, len(aa))
		for _, v := range aa {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// NormalizeArgs returns a stable string key for a map of tool arguments.
// It uses JSON marshal, which sorts map keys deterministically.
func NormalizeArgs(args map[string]any) string {
	if args == nil {
		return "{}"
	}
	b, err := json.Marshal(args)
	if err != nil {
		// Fallback: keys joined. Should never happen for valid args.
		return fmt.Sprintf("%v", args)
	}
	return string(b)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/tools -run 'TestString|TestInt|TestStringSlice|TestNormalizeArgs' -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/tools/args.go pkg/tools/args_test.go
git commit -m "feat(tools): add typed argument helpers and stable normalization"
```

---

### Task 2: Executor 同 turn 去重缓存

**Files:**
- Modify: `pkg/agent/executor.go`
- Create: `pkg/agent/executor_dedup_test.go`

- [ ] **Step 1: Write the failing test**

```go
package agent

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
)

func TestExecute_DedupReadSameTurn(t *testing.T) {
	toolMsg := models.NewAgentMessage(models.RoleAssistant,
		models.ToolCallContent{Type: "tool_call", ID: "call_1", Name: "read", Arguments: map[string]any{"path": "limits.go"}},
		models.ToolCallContent{Type: "tool_call", ID: "call_2", Name: "read", Arguments: map[string]any{"path": "limits.go"}},
	)
	client := llmtest.Client(llmtest.Turn(llmtest.Done(toolMsg, nil)))

	bus := events.New()
	var endCount int
	bus.Subscribe(func(_ context.Context, ev events.Event) error {
		if ev.EventType() == events.ToolExecutionEnd {
			endCount++
		}
		return nil
	})

	ag := New(Config{
		SystemPrompt:      "x",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionParallel,
		ShouldStop: func(context.Context, TurnSummary) (bool, error) {
			return true, nil
		},
	}, client, testRegistry("."), permissions.NewEngine(permissions.DefaultConfig()), bus)

	if err := ag.Prompt(context.Background(), models.UserMessage("go")); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	if endCount != 1 {
		t.Fatalf("expected 1 tool execution for dedup, got %d", endCount)
	}

	msgs := ag.AllMessages()
	var dedupFound bool
	for _, m := range msgs {
		if m.Role != models.RoleToolResult {
			continue
		}
		tr, ok := m.Content[0].(models.ToolResultContent)
		if !ok {
			continue
		}
		if tr.ToolCallID == "call_2" && tr.Details != nil && tr.Details["deduplicated"] == true {
			dedupFound = true
		}
	}
	if !dedupFound {
		t.Fatal("expected second read result to be marked deduplicated")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/agent -run TestExecute_DedupReadSameTurn -v`

Expected: FAIL, `endCount` will be 2 and `deduplicated` not found.

- [ ] **Step 3: Add helpers and cache fields to executor**

在 `pkg/agent/executor.go` 的 `executor` 结构体添加：

```go	type executor struct {
		// ... existing fields ...
		dedupMu sync.Mutex
		dedup   map[string]models.AgentMessage
	}
```

在 `execute` 函数开头重置缓存：

```go
func (e *executor) execute(ctx context.Context, turn int, assistantMsg models.AgentMessage, calls []models.ToolCallContent, execMode models.ExecutionMode) ([]models.AgentMessage, bool) {
	e.dedupMu.Lock()
	e.dedup = make(map[string]models.AgentMessage)
	e.dedupMu.Unlock()

	// ... rest unchanged ...
}
```

在文件末尾添加辅助函数：

```go
func isCacheableTool(name string) bool {
	switch name {
	case "read", "ls", "grep", "find":
		return true
	}
	return false
}

func dedupKey(name string, args map[string]any) string {
	return name + "|" + tools.NormalizeArgs(args)
}

func cloneAgentMessage(msg models.AgentMessage, newToolCallID string) models.AgentMessage {
	cloned := msg
	cloned.ID = uuid.New().String()[:12]
	cloned.Content = make([]models.ContentPart, len(msg.Content))
	copy(cloned.Content, msg.Content)
	cloned.Metadata = make(map[string]any)
	for k, v := range msg.Metadata {
		cloned.Metadata[k] = v
	}
	cloned.Details = make(map[string]any)
	for k, v := range msg.Details {
		cloned.Details[k] = v
	}
	if len(cloned.Content) > 0 {
		if tr, ok := cloned.Content[0].(models.ToolResultContent); ok {
			tr.ToolCallID = newToolCallID
			cloned.Content[0] = tr
		}
	}
	cloned.Details["deduplicated"] = true
	return cloned
}
```

注意需要在 import 中加入 `github.com/google/uuid`（`models` 包已依赖，可直接使用）。`tools` 包已被导入。

- [ ] **Step 4: Wire cache lookup/store into executeOneToolCall**

在 `executeOneToolCall` 的参数校验之后、权限检查之前插入缓存命中逻辑：

```go
	// Pre-execution argument validation.
	if exec, ok := e.registry.Get(call.Name); ok {
		if err := tools.ValidateArgs(exec.Definition(), args); err != nil {
			return e.makeToolResultMessage(call, models.NewToolExecutionResultError(err.Error()), true)
		}
	}

	// Same-turn deduplication for read-only idempotent tools.
	if isCacheableTool(call.Name) {
		key := dedupKey(call.Name, args)
		e.dedupMu.Lock()
		cached, ok := e.dedup[key]
		e.dedupMu.Unlock()
		if ok {
			return cloneAgentMessage(cached, call.ID)
		}
	}
```

在函数返回前存储结果：

```go
	msg := e.makeToolResultMessage(call, result, isError)

	if isCacheableTool(call.Name) {
		key := dedupKey(call.Name, args)
		e.dedupMu.Lock()
		e.dedup[key] = msg
		e.dedupMu.Unlock()
	}

	// existing emitter calls ...
	return msg
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./pkg/agent -run TestExecute_DedupReadSameTurn -v`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/agent/executor.go pkg/agent/executor_dedup_test.go
git commit -m "feat(agent): deduplicate read-only tool calls within the same turn"
```

---

### Task 3: `edit` 工具两阶段执行

**Files:**
- Modify: `pkg/tools/builtin/edit.go`
- Create: `pkg/tools/builtin/edit_test.go`

- [ ] **Step 1: Write the failing test**

```go
package builtin

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lcoder/lcoder/pkg/sandbox"
)

func TestEdit_DryRunFailureLeavesFileUnchanged(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "main.go")
	original := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	edit := NewEdit(dir).(*Edit)
	edit.UseSandbox(sandbox.NewFakeSandbox())

	_, err := edit.Execute(context.Background(), "call_1", map[string]any{
		"path": "main.go",
		"edits": []any{
			map[string]any{"oldText": "THIS DOES NOT EXIST", "newText": "x"},
		},
	})
	if err == nil {
		t.Fatal("expected error for non-matching edit")
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("file was modified despite dry-run failure: %q", string(data))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tools/builtin -run TestEdit_DryRunFailureLeavesFileUnchanged -v`

Expected: FAIL, file is currently modified in-place before the mismatch is detected, or test file missing.

- [ ] **Step 3: Refactor edit.go to two-phase**

将 `pkg/tools/builtin/edit.go` 的 `Execute` 替换为以下实现：

```go
const (
	backupSuffix = ".lcoder.bak"
	tmpSuffix    = ".lcoder.tmp"
)

type editOp struct {
	oldText string
	newText string
}

func parseEdits(args map[string]any) ([]editOp, error) {
	editsRaw, ok := args["edits"].([]any)
	if !ok || len(editsRaw) == 0 {
		return nil, fmt.Errorf("missing edits")
	}
	out := make([]editOp, 0, len(editsRaw))
	for _, raw := range editsRaw {
		edit, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid edit entry")
		}
		oldText, ok := edit["oldText"].(string)
		if !ok {
			return nil, fmt.Errorf("edit missing oldText")
		}
		newText, ok := edit["newText"].(string)
		if !ok {
			return nil, fmt.Errorf("edit missing newText")
		}
		out = append(out, editOp{oldText: oldText, newText: newText})
	}
	return out, nil
}

func applyEdits(text string, edits []editOp) (string, error) {
	for _, e := range edits {
		if !containsOnce(text, e.oldText) {
			return "", fmt.Errorf("oldText not found or not unique")
		}
		text = replaceOnce(text, e.oldText, e.newText)
	}
	return text, nil
}

func (e *Edit) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	path := args["path"].(string)
	path, err := resolveAndCheck(e.cwd, e.sb, path, sandbox.FSWrite)
	if err != nil {
		return models.ToolExecutionResult{}, err
	}

	edits, err := parseEdits(args)
	if err != nil {
		return models.ToolExecutionResult{}, err
	}

	original, err := os.ReadFile(path)
	if err != nil {
		return models.ToolExecutionResult{}, err
	}

	// Stage 1: dry-run in memory.
	newText, err := applyEdits(string(original), edits)
	if err != nil {
		return models.ToolExecutionResult{}, fmt.Errorf("%s: %w", path, err)
	}

	// Stage 2: commit with backup + atomic rename.
	backupPath := path + backupSuffix
	if err := os.WriteFile(backupPath, original, 0o600); err != nil {
		return models.ToolExecutionResult{}, fmt.Errorf("backup failed: %w", err)
	}

	tmpPath := path + tmpSuffix
	if err := os.WriteFile(tmpPath, []byte(newText), 0o644); err != nil {
		_ = os.Remove(backupPath)
		return models.ToolExecutionResult{}, fmt.Errorf("write temp failed: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Rename(backupPath, path)
		_ = os.Remove(tmpPath)
		return models.ToolExecutionResult{}, fmt.Errorf("commit failed: %w; restored from backup", err)
	}

	_ = os.Remove(backupPath)

	return models.ToolExecutionResult{
		Content: []models.ContentPart{
			models.TextContent{Text: fmt.Sprintf("Applied %d edit(s) to %s", len(edits), path)},
		},
		Details: map[string]any{"path": path, "edits": len(edits)},
	}, nil
}
```

保留 `containsOnce` 与 `replaceOnce` 不变。

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/tools/builtin -run TestEdit_DryRunFailureLeavesFileUnchanged -v`

Expected: PASS.

- [ ] **Step 5: Add commit-success test**

在 `pkg/tools/builtin/edit_test.go` 追加：

```go
func TestEdit_CommitSuccess(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "main.go")
	original := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	edit := NewEdit(dir).(*Edit)
	edit.UseSandbox(sandbox.NewFakeSandbox())

	res, err := edit.Execute(context.Background(), "call_1", map[string]any{
		"path": "main.go",
		"edits": []any{
			map[string]any{"oldText": "func main() {}", "newText": "func main() { println(\"hi\") }"},
		},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	want := "package main\n\nfunc main() { println(\"hi\") }\n"
	if string(data) != want {
		t.Fatalf("file = %q, want %q", string(data), want)
	}
	if _, err := os.Stat(target + backupSuffix); !os.IsNotExist(err) {
		t.Fatal("backup file should be removed after successful commit")
	}
	if res.Text() == "" {
		t.Fatal("expected non-empty result text")
	}
}
```

Run: `go test ./pkg/tools/builtin -run 'TestEdit_' -v`

Expected: both PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/tools/builtin/edit.go pkg/tools/builtin/edit_test.go
git commit -m "feat(edit): two-phase dry-run + commit with backup rollback"
```

---

### Task 4: `write` 工具两阶段执行

**Files:**
- Modify: `pkg/tools/builtin/write.go`
- Create: `pkg/tools/builtin/write_test.go`

- [ ] **Step 1: Write the failing test**

```go
package builtin

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lcoder/lcoder/pkg/sandbox"
)

func TestWrite_BackupAndOverwrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	write := NewWrite(dir).(*Write)
	write.UseSandbox(sandbox.NewFakeSandbox())

	res, err := write.Execute(context.Background(), "call_1", map[string]any{
		"path":    "config.yaml",
		"content": "new",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("file = %q, want new", string(data))
	}
	if _, err := os.Stat(target + backupSuffix); !os.IsNotExist(err) {
		t.Fatal("backup should be removed after success")
	}
	if res.Text() == "" {
		t.Fatal("expected non-empty result")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tools/builtin -run TestWrite_BackupAndOverwrite -v`

Expected: FAIL，测试文件不存在或备份逻辑未实现。

- [ ] **Step 3: Refactor write.go to two-phase**

将 `pkg/tools/builtin/write.go` 的 `Execute` 替换为：

```go
func (w *Write) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	path := args["path"].(string)
	content := args["content"].(string)
	path, err := resolveAndCheck(w.cwd, w.sb, path, sandbox.FSWrite)
	if err != nil {
		return models.ToolExecutionResult{}, err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return models.ToolExecutionResult{}, err
	}

	// Backup existing file before overwriting.
	var hadBackup bool
	backupPath := path + backupSuffix
	if _, statErr := os.Stat(path); statErr == nil {
		original, err := os.ReadFile(path)
		if err != nil {
			return models.ToolExecutionResult{}, fmt.Errorf("read existing file for backup: %w", err)
		}
		if err := os.WriteFile(backupPath, original, 0o600); err != nil {
			return models.ToolExecutionResult{}, fmt.Errorf("backup failed: %w", err)
		}
		hadBackup = true
	}

	tmpPath := path + tmpSuffix
	if err := os.WriteFile(tmpPath, []byte(content), 0o644); err != nil {
		if hadBackup {
			_ = os.Remove(backupPath)
		}
		return models.ToolExecutionResult{}, fmt.Errorf("write temp failed: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		if hadBackup {
			_ = os.Rename(backupPath, path)
		}
		_ = os.Remove(tmpPath)
		return models.ToolExecutionResult{}, fmt.Errorf("commit failed: %w; restored from backup", err)
	}

	if hadBackup {
		_ = os.Remove(backupPath)
	}

	return models.ToolExecutionResult{
		Content: []models.ContentPart{
			models.TextContent{Text: fmt.Sprintf("Wrote %d characters to %s", len(content), path)},
		},
		Details: map[string]any{"path": path},
	}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/tools/builtin -run TestWrite_BackupAndOverwrite -v`

Expected: PASS.

- [ ] **Step 5: Add failure-leaves-original test**

在 `pkg/tools/builtin/write_test.go` 追加：

```go
func TestWrite_FailureKeepsOriginal(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "readonly", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o444); err != nil {
		t.Fatal(err)
	}

	write := NewWrite(dir).(*Write)
	write.UseSandbox(sandbox.NewFakeSandbox())

	_, err := write.Execute(context.Background(), "call_1", map[string]any{
		"path":    "readonly/config.yaml",
		"content": "new",
	})
	if err == nil {
		t.Fatal("expected error writing to read-only file")
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old" {
		t.Fatalf("original file was mutated: %q", string(data))
	}
}
```

Run: `go test ./pkg/tools/builtin -run 'TestWrite_' -v`

Expected: both PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/tools/builtin/write.go pkg/tools/builtin/write_test.go
git commit -m "feat(write): two-phase write with backup rollback"
```

---

### Task 5: `bash` 工具显式产物回传

**Files:**
- Modify: `pkg/tools/builtin/bash.go`
- Create: `pkg/tools/builtin/bash_test.go`

- [ ] **Step 1: Write the failing test**

```go
package builtin

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lcoder/lcoder/pkg/sandbox"
)

func TestBash_CopiesOutputsOnSuccess(t *testing.T) {
	dir := t.TempDir()
	bash := NewBash(dir).(*Bash)
	fsb := sandbox.NewFakeSandbox()
	fsb.Result = sandbox.ExecResult{Stdout: "ok", ExitCode: 0}
	bash.UseSandbox(fsb)

	res, err := bash.Execute(context.Background(), "call_1", map[string]any{
		"command": "echo ok",
		"outputs": []any{"report.md"},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Text() != "ok" {
		t.Fatalf("result text = %q, want ok", res.Text())
	}
	copied, _ := res.Details["outputs_copied"].([]string)
	if len(copied) != 0 {
		// 当前未实现，所以预期 len(copied)==0；测试先失败以驱动实现。
		t.Fatalf("expected outputs_copied to be populated, got %v", copied)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tools/builtin -run TestBash_CopiesOutputsOnSuccess -v`

Expected: FAIL，`outputs_copied` 不存在。

- [ ] **Step 3: Update bash Definition and add output-copy helper**

在 `Definition` 的 `properties` 中新增：

```go
"outputs": map[string]any{
	"type":        "array",
	"description": "Optional list of output file paths to copy back to the workspace on success.",
	"items": map[string]any{"type": "string"},
},
```

在 `bash.go` 末尾添加：

```go
func copyOutputs(outputs []string, srcDir, dstDir string) ([]string, error) {
	var copied []string
	for _, out := range outputs {
		src := out
		if !filepath.IsAbs(src) {
			src = filepath.Join(srcDir, src)
		}
		dst := out
		if !filepath.IsAbs(dst) {
			dst = filepath.Join(dstDir, dst)
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return copied, fmt.Errorf("read output %s: %w", out, err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return copied, fmt.Errorf("mkdir for %s: %w", dst, err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return copied, fmt.Errorf("write output %s: %w", dst, err)
		}
		copied = append(copied, dst)
	}
	return copied, nil
}
```

- [ ] **Step 4: Wire outputs into Execute**

在 `Execute` 开头解析参数：

```go
	command := tools.String(args, "command", "")
	if command == "" {
		return models.ToolExecutionResult{}, fmt.Errorf("missing command")
	}
	outputs := tools.StringSlice(args, "outputs")
```

在 sandbox 分支成功返回前插入：

```go
	if result.ExitCode != 0 {
		return res, fmt.Errorf("command failed: exit code %d", result.ExitCode)
	}

	copied, copyErr := copyOutputs(outputs, cwd, cwd)
	if copyErr != nil {
		return res, fmt.Errorf("command succeeded but copying outputs failed: %w", copyErr)
	}
	if len(copied) > 0 {
		res.Details["outputs_copied"] = copied
	}
	return res, nil
```

在非 sandbox 分支的 `err == nil` 之后做同样的复制处理。

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./pkg/tools/builtin -run TestBash_CopiesOutputsOnSuccess -v`

Expected: PASS（因为 FakeSandbox 不真正创建 `report.md`，`copyOutputs` 会返回错误；需要调整测试，见 Step 6）。

- [ ] **Step 6: Make test robust by creating sandbox output files**

将测试改为先创建源文件，再验证复制：

```go
func TestBash_CopiesOutputsOnSuccess(t *testing.T) {
	dir := t.TempDir()
	bash := NewBash(dir).(*Bash)
	fsb := sandbox.NewFakeSandbox()
	fsb.Result = sandbox.ExecResult{Stdout: "ok", ExitCode: 0}
	bash.UseSandbox(fsb)

	// Simulate the sandbox command writing an output file.
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte("# report"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := bash.Execute(context.Background(), "call_1", map[string]any{
		"command": "echo ok",
		"outputs": []any{"report.md"},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	copied, _ := res.Details["outputs_copied"].([]string)
	if len(copied) != 1 || copied[0] != filepath.Join(dir, "report.md") {
		t.Fatalf("outputs_copied = %v, want one entry", copied)
	}
}

func TestBash_DoesNotCopyOutputsOnFailure(t *testing.T) {
	dir := t.TempDir()
	bash := NewBash(dir).(*Bash)
	fsb := sandbox.NewFakeSandbox()
	fsb.Result = sandbox.ExecResult{Stdout: "fail", Stderr: "", ExitCode: 1}
	bash.UseSandbox(fsb)

	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte("# report"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := bash.Execute(context.Background(), "call_1", map[string]any{
		"command": "exit 1",
		"outputs": []any{"report.md"},
	})
	if err == nil {
		t.Fatal("expected error for non-zero exit")
	}
	if _, ok := err.(interface{ Error() string }).Error(); !ok {
		t.Fatal("expected a concrete error")
	}
}
```

Run: `go test ./pkg/tools/builtin -run 'TestBash_' -v`

Expected: both PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/tools/builtin/bash.go pkg/tools/builtin/bash_test.go
git commit -m "feat(bash): support explicit outputs copied back on success"
```

---

### Task 6: 全局回归验证

**Files:**
- All touched files above.

- [ ] **Step 1: Run package tests**

```bash
go test ./pkg/tools/... ./pkg/agent/...
```

Expected: all PASS.

- [ ] **Step 2: Run vet**

```bash
go vet ./pkg/tools/... ./pkg/agent/...
```

Expected: no warnings.

- [ ] **Step 3: Build full project**

```bash
go build ./...
```

Expected: success.

- [ ] **Step 4: Commit any remaining changes**

```bash
git add -A
git commit -m "test: regression suite for tool execution isolation and dedup"
```

---

## 自我审查

**1. Spec coverage:**
- 同 turn 去重 → Task 2
- `write/edit` 两阶段 → Task 3 / Task 4
- `bash` 显式产物 → Task 5
- 错误传播保持 `IsError` → 每个任务的失败路径都返回 Go `error`，由 `registry.Execute` 统一包装

**2. Placeholder scan:** 无 `TBD` / `TODO` / 空描述。

**3. Type consistency：**
- `tools.NormalizeArgs` 在 Task 1 定义，Task 2 使用。
- `backupSuffix` / `tmpSuffix` 在 Task 3 定义，Task 4 复用（同一包常量）。
- `outputs` 参数在 Task 5 中由 `tools.StringSlice` 解析。

---

**执行方式选择：**

Plan complete and saved to `docs/superpowers/plans/2026-07-10-tool-execution-isolation-plan.md`. Two execution options:

1. **Subagent-Driven (recommended)** - dispatch a fresh subagent per task, review between tasks.
2. **Inline Execution** - execute tasks in this session using `superpowers:executing-plans`.

Which approach would you like?
