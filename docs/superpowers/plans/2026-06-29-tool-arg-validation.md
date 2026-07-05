# 工具执行前参数校验 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在工具真正执行前依据其 JSON Schema 校验 LLM 传入的参数(必填 + 顶层类型),校验失败时不发出任何 TUI 工具事件,而是把纠正提示作为错误 tool_result 反馈给 LLM 让其下一轮重试。

**Architecture:** 新增纯函数 `tools.ValidateArgs`(无 LLM 依赖,独立单测);在 `agent.executeOneToolCall` 的第一个事件 emit 之前调用它,失败则直接返回错误 tool_result 而不 emit,从而让失败尝试在实时 TUI 中不可见。

**Tech Stack:** Go;标准库;现有 `pkg/models`、`pkg/tools`、`pkg/agent`、`pkg/events` 包。

> 注:本项目 `docs/` 已 gitignore;本计划仅本地留存。配套设计文档:`docs/superpowers/specs/2026-06-29-tool-arg-validation-design.md`。

---

### Task 1: `ValidateArgs` —— 必填字段校验

**Files:**
- Create: `pkg/tools/validate.go`
- Test: `pkg/tools/validate_test.go`

- [ ] **Step 1: Write the failing test**

创建 `pkg/tools/validate_test.go`:

```go
package tools

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func defWith(params map[string]any) models.ToolDefinition {
	return models.ToolDefinition{Name: "demo", Parameters: params}
}

func TestValidateArgs_MissingRequired(t *testing.T) {
	def := defWith(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":  map[string]any{"type": "string"},
			"edits": map[string]any{"type": "array"},
		},
		"required": []string{"path", "edits"},
	})
	err := ValidateArgs(def, map[string]any{"path": "main.go"})
	if err == nil {
		t.Fatal("expected error for missing required field 'edits'")
	}
	if !contains(err.Error(), "edits") {
		t.Fatalf("error should name the missing field, got %q", err.Error())
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tools/ -run TestValidateArgs_MissingRequired -v`
Expected: FAIL — `undefined: ValidateArgs`(编译错误)。

- [ ] **Step 3: Write minimal implementation**

创建 `pkg/tools/validate.go`:

```go
package tools

import (
	"fmt"
	"strings"

	"github.com/lcoder/lcoder/pkg/models"
)

// ValidateArgs checks args against a tool's JSON-Schema parameters. It verifies
// that required fields are present and that the top-level type of each provided
// field matches its declared JSON type. It returns nil when args are valid, or
// an LLM-friendly error describing the first problem found. Schemas that are not
// a recognizable object schema (no "properties") pass through unchecked.
func ValidateArgs(def models.ToolDefinition, args map[string]any) error {
	props, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		return nil // not a recognizable object schema — degrade gracefully
	}

	for _, name := range requiredFields(def.Parameters) {
		if _, present := args[name]; !present {
			return fmt.Errorf("invalid arguments for %q: missing required field %q%s",
				def.Name, name, expectedSuffix(props, name, args))
		}
	}
	return nil
}

// requiredFields extracts the "required" list, tolerating both []string and
// []any (the latter is what JSON unmarshaling produces).
func requiredFields(params map[string]any) []string {
	switch r := params["required"].(type) {
	case []string:
		return r
	case []any:
		out := make([]string, 0, len(r))
		for _, v := range r {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// expectedSuffix renders " (expected <type>); provided: a, b" to help the LLM
// self-correct. The type fragment is omitted when the field declares none.
func expectedSuffix(props map[string]any, name string, args map[string]any) string {
	var b strings.Builder
	if spec, ok := props[name].(map[string]any); ok {
		if t, ok := spec["type"].(string); ok {
			fmt.Fprintf(&b, " (expected %s)", t)
		}
	}
	provided := make([]string, 0, len(args))
	for k := range args {
		provided = append(provided, k)
	}
	if len(provided) > 0 {
		fmt.Fprintf(&b, "; provided: %s", strings.Join(provided, ", "))
	}
	return b.String()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/tools/ -run TestValidateArgs_MissingRequired -v`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add pkg/tools/validate.go pkg/tools/validate_test.go
git commit -m "feat(tools): validate required tool args before execution

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 2: `ValidateArgs` —— 顶层类型校验 + 宽松数值

**Files:**
- Modify: `pkg/tools/validate.go`
- Test: `pkg/tools/validate_test.go`

- [ ] **Step 1: Write the failing test**

在 `pkg/tools/validate_test.go` 追加:

```go
func TestValidateArgs_TypeMismatch(t *testing.T) {
	def := defWith(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
			"line": map[string]any{"type": "number"},
		},
		"required": []string{"path"},
	})
	err := ValidateArgs(def, map[string]any{"path": "main.go", "line": "42"})
	if err == nil {
		t.Fatal("expected error: line should be number, got string")
	}
	if !contains(err.Error(), "line") {
		t.Fatalf("error should name the offending field, got %q", err.Error())
	}
}

func TestValidateArgs_LenientNumbers(t *testing.T) {
	def := defWith(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"line": map[string]any{"type": "number"},
		},
	})
	for _, v := range []any{float64(42), int(42), int64(42), json.Number("42")} {
		if err := ValidateArgs(def, map[string]any{"line": v}); err != nil {
			t.Fatalf("number type should accept %T, got error %v", v, err)
		}
	}
}

func TestValidateArgs_Valid(t *testing.T) {
	def := defWith(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":  map[string]any{"type": "string"},
			"edits": map[string]any{"type": "array"},
		},
		"required": []string{"path", "edits"},
	})
	args := map[string]any{"path": "main.go", "edits": []any{map[string]any{}}}
	if err := ValidateArgs(def, args); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}
```

`json` 需要导入到测试文件:在 `validate_test.go` 顶部 import 块加入 `"encoding/json"`。

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tools/ -run TestValidateArgs_TypeMismatch -v`
Expected: FAIL — 当前 `ValidateArgs` 只检查必填,类型不符未被捕获,`err == nil`,测试在 `t.Fatal` 处失败。

- [ ] **Step 3: Write minimal implementation**

修改 `pkg/tools/validate.go`:在必填检查循环之后、`return nil` 之前插入类型检查;并给文件 import 块加入 `"encoding/json"`(`typeMatches`/`jsonTypeOf` 用到 `json.Number`)。

import 块改为:

```go
import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lcoder/lcoder/pkg/models"
)
```

`ValidateArgs` 改为:

```go
func ValidateArgs(def models.ToolDefinition, args map[string]any) error {
	props, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		return nil // not a recognizable object schema — degrade gracefully
	}

	for _, name := range requiredFields(def.Parameters) {
		if _, present := args[name]; !present {
			return fmt.Errorf("invalid arguments for %q: missing required field %q%s",
				def.Name, name, expectedSuffix(props, name, args))
		}
	}

	for name, value := range args {
		spec, ok := props[name].(map[string]any)
		if !ok {
			continue // field not described by schema — nothing to check
		}
		wantType, ok := spec["type"].(string)
		if !ok {
			continue // schema declares no type for this field
		}
		if !typeMatches(wantType, value) {
			return fmt.Errorf("invalid arguments for %q: field %q must be %s, got %s",
				def.Name, name, wantType, jsonTypeOf(value))
		}
	}
	return nil
}

// typeMatches reports whether value's Go type satisfies the JSON type name.
// Numbers are accepted leniently because providers deserialize them
// inconsistently (float64, int, int64, or json.Number).
func typeMatches(jsonType string, value any) bool {
	switch jsonType {
	case "string":
		_, ok := value.(string)
		return ok
	case "number", "integer":
		switch value.(type) {
		case float64, int, int64, json.Number:
			return true
		}
		return false
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	}
	return true // unknown JSON type — do not block
}

// jsonTypeOf names value's apparent JSON type for error messages.
func jsonTypeOf(value any) string {
	switch value.(type) {
	case string:
		return "string"
	case float64, int, int64, json.Number:
		return "number"
	case bool:
		return "boolean"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return "unknown"
}
```

`json` 现在被 `typeMatches`/`jsonTypeOf` 真正使用,Task 1 的 import 块需相应加上(见上方 import 块)。

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/tools/ -v`
Expected: PASS（全部 `TestValidateArgs_*`）。

- [ ] **Step 5: Commit**

```bash
git add pkg/tools/validate.go pkg/tools/validate_test.go
git commit -m "feat(tools): validate top-level arg types with lenient numbers

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 3: `ValidateArgs` —— 降级与边界(空 schema / 无 required / 嵌套不深入)

**Files:**
- Test: `pkg/tools/validate_test.go`

- [ ] **Step 1: Write the failing test**

在 `pkg/tools/validate_test.go` 追加:

```go
func TestValidateArgs_DegradesWithoutProperties(t *testing.T) {
	// No "properties" key — e.g. an unusual MCP/extension schema.
	def := defWith(map[string]any{"type": "object"})
	if err := ValidateArgs(def, map[string]any{"anything": 1}); err != nil {
		t.Fatalf("schema without properties should pass through, got %v", err)
	}
	// Entirely empty parameters.
	if err := ValidateArgs(defWith(map[string]any{}), map[string]any{}); err != nil {
		t.Fatalf("empty schema should pass through, got %v", err)
	}
}

func TestValidateArgs_NoRequiredOnlyTypes(t *testing.T) {
	def := defWith(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
		},
	})
	// Missing optional field is fine.
	if err := ValidateArgs(def, map[string]any{}); err != nil {
		t.Fatalf("missing optional field should pass, got %v", err)
	}
	// But a present field with wrong type still fails.
	if err := ValidateArgs(def, map[string]any{"path": 123}); err == nil {
		t.Fatal("present field with wrong type should fail")
	}
}

func TestValidateArgs_NestedNotInspected(t *testing.T) {
	def := defWith(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"edits": map[string]any{"type": "array"},
		},
		"required": []string{"edits"},
	})
	// Array items are malformed, but top-level type (array) is correct, so the
	// top-level validator accepts it — nesting is out of scope by design.
	args := map[string]any{"edits": []any{"not-an-object"}}
	if err := ValidateArgs(def, args); err != nil {
		t.Fatalf("nested item errors must not be caught at top level, got %v", err)
	}
}

func TestValidateArgs_RequiredAsAnySlice(t *testing.T) {
	// JSON unmarshaling yields []any for "required", not []string.
	def := defWith(map[string]any{
		"type":       "object",
		"properties": map[string]any{"path": map[string]any{"type": "string"}},
		"required":   []any{"path"},
	})
	if err := ValidateArgs(def, map[string]any{}); err == nil {
		t.Fatal("required given as []any should still be enforced")
	}
}
```

- [ ] **Step 2: Run test to verify it fails or passes**

Run: `go test ./pkg/tools/ -run TestValidateArgs -v`
Expected: PASS。这些是边界确认测试,验证 Task 1–2 实现的行为契约(降级、可选字段、不递归、`required` 为 `[]any`)。若任一 FAIL,说明实现与契约不符,需回到 `validate.go` 修正(而非改测试)。

- [ ] **Step 3: 实现已覆盖,无需新代码**

Task 1–2 的实现已满足这些契约。如有 FAIL,定位 `validate.go` 对应分支修正。

- [ ] **Step 4: Run the full package tests**

Run: `go test ./pkg/tools/ -v`
Expected: PASS（全部）。

- [ ] **Step 5: Commit**

```bash
git add pkg/tools/validate_test.go
git commit -m "test(tools): pin ValidateArgs boundary contracts

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 4: 接入 `executeOneToolCall` —— 校验失败不 emit、反馈错误

**Files:**
- Modify: `pkg/agent/toolexec.go:79-107`(`executeOneToolCall` 开头部分)
- Test: `pkg/agent/toolexec_test.go`

- [ ] **Step 1: Write the failing test**

创建 `pkg/agent/toolexec_test.go`:

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

// schemaToolMsg builds an assistant message that calls "edit" with the given
// arguments. "edit" requires both "path" and "edits".
func schemaToolMsg(args map[string]any) models.AgentMessage {
	return models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Type: "tool_call", ID: "call_1", Name: "edit", Arguments: args,
	})
}

func TestExecuteToolCall_InvalidArgs_NoToolEvents(t *testing.T) {
	// Assistant calls edit with a missing required field ("edits").
	toolMsg := schemaToolMsg(map[string]any{"path": "main.go"})
	client := llmtest.Client(llmtest.Turn(llmtest.Done(toolMsg, nil)))

	bus := events.New()
	var sawStart, sawEnd bool
	bus.Subscribe(func(_ context.Context, ev events.Event) error {
		switch ev.EventType() {
		case events.ToolExecutionStart:
			sawStart = true
		case events.ToolExecutionEnd:
			sawEnd = true
		}
		return nil
	})

	ag := New(Config{
		SystemPrompt:      "x",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		MaxTurns:          1,
		ToolExecutionMode: models.ExecutionParallel,
	}, client, testRegistry("."), permissions.NewEngine(permissions.DefaultConfig()), bus)

	if err := ag.Prompt(context.Background(), models.UserMessage("go")); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	if sawStart || sawEnd {
		t.Fatalf("validation failure must not emit tool events (start=%v end=%v)", sawStart, sawEnd)
	}

	// The failed call must surface as an error tool_result in context so the LLM
	// can correct on the next turn.
	msgs := ag.AllMessages()
	var foundErr bool
	for _, m := range msgs {
		if m.Role != models.RoleToolResult {
			continue
		}
		if rc, ok := m.Content[0].(models.ToolResultContent); ok && rc.IsError {
			foundErr = true
		}
	}
	if !foundErr {
		t.Fatal("expected an error tool_result fed back for the invalid call")
	}
}
```

`testRegistry` 复用 `pkg/agent/loop_test.go:17` 已有的同包 helper(注册全部 builtin,含 `edit`)。

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/agent/ -run TestExecuteToolCall_InvalidArgs_NoToolEvents -v`
Expected: FAIL — 当前实现先 emit `ToolExecutionStart`,`sawStart == true`,在第一个 `t.Fatalf` 处失败。

- [ ] **Step 3: Write minimal implementation**

修改 `pkg/agent/toolexec.go` 的 `executeOneToolCall`。当前开头为:

```go
func (a *Agent) executeOneToolCall(ctx context.Context, turn int, assistantMsg models.AgentMessage, call models.ToolCallContent) models.AgentMessage {
	_ = a.bus.Emit(ctx, events.ToolExecutionStartEvent{
		Base:       events.Base{Type: events.ToolExecutionStart, Turn: turn},
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Args:       call.Arguments,
	})

	// Validate / prepare arguments.
	args := call.Arguments
	if args == nil {
		args = make(map[string]any)
	}

	// Permission check via hook.
```

改为(把 args 归一上移到最前;在 emit 之前插入校验分支):

```go
func (a *Agent) executeOneToolCall(ctx context.Context, turn int, assistantMsg models.AgentMessage, call models.ToolCallContent) models.AgentMessage {
	// Normalize arguments first so validation sees a non-nil map.
	args := call.Arguments
	if args == nil {
		args = make(map[string]any)
	}

	// Pre-execution argument validation. On failure we do NOT emit any tool
	// events: the failed attempt stays invisible in the live TUI, and the
	// error tool_result is fed back so the LLM can self-correct next turn.
	// Unknown tools fall through to the normal path below (handled by
	// registry.Execute), since that is not a parameter error.
	if exec, ok := a.registry.Get(call.Name); ok {
		if err := tools.ValidateArgs(exec.Definition(), args); err != nil {
			return a.makeToolResultMessage(call, models.NewToolResultError(err.Error()), true)
		}
	}

	_ = a.bus.Emit(ctx, events.ToolExecutionStartEvent{
		Base:       events.Base{Type: events.ToolExecutionStart, Turn: turn},
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Args:       call.Arguments,
	})

	// Permission check via hook.
```

注意删除原先 emit 之后那段已上移的 `// Validate / prepare arguments.` 注释与 `args := call.Arguments` / nil 检查块(避免重复声明 `args`)。

在 `pkg/agent/toolexec.go` 的 import 块加入 `"github.com/lcoder/lcoder/pkg/tools"`:

```go
import (
	"context"
	"sync"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/tools"
)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/agent/ -run TestExecuteToolCall_InvalidArgs_NoToolEvents -v`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add pkg/agent/toolexec.go pkg/agent/toolexec_test.go
git commit -m "feat(agent): validate tool args before emitting tool events

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 5: 回归 —— 合法参数仍正常 emit 与执行

**Files:**
- Test: `pkg/agent/toolexec_test.go`

- [ ] **Step 1: Write the failing test**

在 `pkg/agent/toolexec_test.go` 追加。用 `ls`(无必填参数,空 args 合法)确认正常路径不受影响:

```go
func TestExecuteToolCall_ValidArgs_EmitsEvents(t *testing.T) {
	toolMsg := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Type: "tool_call", ID: "call_1", Name: "ls", Arguments: map[string]any{},
	})
	client := llmtest.Client(llmtest.Turn(llmtest.Done(toolMsg, nil)))

	bus := events.New()
	var sawStart, sawEnd bool
	bus.Subscribe(func(_ context.Context, ev events.Event) error {
		switch ev.EventType() {
		case events.ToolExecutionStart:
			sawStart = true
		case events.ToolExecutionEnd:
			sawEnd = true
		}
		return nil
	})

	ag := New(Config{
		SystemPrompt:      "x",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		MaxTurns:          1,
		ToolExecutionMode: models.ExecutionParallel,
	}, client, testRegistry("."), permissions.NewEngine(permissions.DefaultConfig()), bus)

	if err := ag.Prompt(context.Background(), models.UserMessage("go")); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	if !sawStart || !sawEnd {
		t.Fatalf("valid call must emit tool events (start=%v end=%v)", sawStart, sawEnd)
	}
}
```

- [ ] **Step 2: Run test to verify it fails or passes**

Run: `go test ./pkg/agent/ -run TestExecuteToolCall_ValidArgs_EmitsEvents -v`
Expected: PASS（Task 4 的实现对合法参数不改变行为,本测试锁定该回归保证）。若 FAIL,说明 Task 4 误伤了正常路径,回到 `toolexec.go` 修正。

- [ ] **Step 3: 无需新代码**

行为已由 Task 4 实现保证,此任务仅加回归测试。

- [ ] **Step 4: Run the agent package tests**

Run: `go test ./pkg/agent/ -v`
Expected: PASS（全部,含既有 `TestAgentToolCall` 等）。

- [ ] **Step 5: Commit**

```bash
git add pkg/agent/toolexec_test.go
git commit -m "test(agent): lock valid-args path still emits tool events

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 6: 全量验证

**Files:** 无改动,仅验证。

- [ ] **Step 1: Run the affected packages**

Run: `go test ./pkg/tools/... ./pkg/agent/...`
Expected: `ok`（两个包及子包全部通过)。

- [ ] **Step 2: Build the binary**

Run: `go build ./cmd/lcoder/`
Expected: 无输出(编译成功)。

- [ ] **Step 3: Vet**

Run: `go vet ./pkg/tools/... ./pkg/agent/...`
Expected: 无输出。

> 说明:不运行 `go test ./...` —— 仓库存在与本改动无关的预置依赖问题(`go mod tidy` 会拉入 Shannon embeddings 依赖)。按既定约定,只跑受影响包的测试。
