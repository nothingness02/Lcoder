# Task 系统 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 LLM 通过一个 `todo_write` 内置工具声明任务清单,TUI 把清单渲染到右侧固定宽度侧栏,实时反映 agent 的计划与进度。

**Architecture:** 工具层只校验 + 返回文本摘要,无状态;TUI 复用现有事件通道——监听 `ToolExecutionStartEvent`,当工具名为 `todo_write` 时从其 `Args` 解析任务列表覆盖到 `Model.tasks`,并在 `View()` 用 `JoinHorizontal` 把侧栏拼到主区右侧。任务 schema 的单一真相源放在新包 `pkg/task`,被工具和 TUI 共用。不新增事件类型、不给工具塞 bus 引用。

**Tech Stack:** Go 1.25,bubbletea/lipgloss,现有 `pkg/tools` 注册机制、`pkg/events` 事件总线。

> **Commits:** 按用户全局规则「仅在用户要求时提交」,执行者只在用户许可时 commit;下方 commit 步骤标出的是建议的提交点。

> **Spec:** `docs/superpowers/specs/2026-06-29-task-system-design.md`

---

## File Structure

| 文件 | 职责 | 动作 |
| --- | --- | --- |
| `pkg/task/task.go` | 任务类型、状态常量、`Parse`/`Counts`、工具名常量(schema 单一真相源) | Create |
| `pkg/task/task_test.go` | `Parse` / `Counts` 单测 | Create |
| `pkg/tools/builtin/todo.go` | `todo_write` 工具:Definition + Execute(校验 + 摘要) | Create |
| `pkg/tools/builtin/todo_test.go` | 工具 Definition / Execute 单测 | Create |
| `pkg/tools/builtin/init.go` | 注册 `todo_write` | Modify |
| `pkg/tui/tasksidebar.go` | 侧栏渲染 + 可见性/宽度/解析/切换/重建 helper | Create |
| `pkg/tui/tasksidebar_test.go` | 上述 helper 与渲染单测 | Create |
| `pkg/tui/model.go` | `Model` 新增 `tasks`/`taskSidebarHidden`/`mainWidth` 字段;`updateSizes` 用主区宽度 | Modify |
| `pkg/tui/view.go` | `View()` 横向拼接侧栏;`bottomRegion`/`statusLineView` 用 `mainWidth` | Modify |
| `pkg/tui/events.go` | `handleEvent` 拦截 `todo_write` 事件 | Modify |
| `pkg/tui/keys.go` | `Ctrl+T` 切换;`/tasks` 命令 dispatch;`loadSession` 重建任务 | Modify |
| `pkg/tui/menu.go` | 注册 `/tasks` 命令条目 | Modify |

---

## Task 1: `pkg/task` 包(schema 单一真相源)

**Files:**
- Create: `pkg/task/task.go`
- Test: `pkg/task/task_test.go`

- [ ] **Step 1: 写失败测试**

Create `pkg/task/task_test.go`:

```go
package task

import "testing"

func argsTodos(items ...map[string]any) any {
	out := make([]any, len(items))
	for i, it := range items {
		out[i] = it
	}
	return out
}

func TestParseValid(t *testing.T) {
	raw := argsTodos(
		map[string]any{"text": "read auth", "status": "done"},
		map[string]any{"text": "split handler", "status": "in_progress"},
		map[string]any{"text": "write tests", "status": "pending"},
	)
	tasks, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("want 3 tasks, got %d", len(tasks))
	}
	if tasks[1].Text != "split handler" || tasks[1].Status != StatusInProgress {
		t.Fatalf("task[1] wrong: %+v", tasks[1])
	}
}

func TestParseRejectsBadStatus(t *testing.T) {
	_, err := Parse(argsTodos(map[string]any{"text": "x", "status": "blocked"}))
	if err == nil {
		t.Fatal("expected error for invalid status")
	}
}

func TestParseRejectsEmptyText(t *testing.T) {
	_, err := Parse(argsTodos(map[string]any{"text": "", "status": "pending"}))
	if err == nil {
		t.Fatal("expected error for empty text")
	}
}

func TestParseRejectsNonArray(t *testing.T) {
	if _, err := Parse("nope"); err == nil {
		t.Fatal("expected error for non-array todos")
	}
}

func TestCounts(t *testing.T) {
	tasks := []Task{
		{Text: "a", Status: StatusDone},
		{Text: "b", Status: StatusDone},
		{Text: "c", Status: StatusInProgress},
		{Text: "d", Status: StatusPending},
	}
	done, inProgress, pending := Counts(tasks)
	if done != 2 || inProgress != 1 || pending != 1 {
		t.Fatalf("counts wrong: %d/%d/%d", done, inProgress, pending)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/task/ -run TestParse -v`
Expected: FAIL — `pkg/task` 不存在 / `Parse` undefined。

- [ ] **Step 3: 写实现**

Create `pkg/task/task.go`:

```go
// Package task defines the agent task-list schema shared by the todo_write tool
// and the TUI sidebar. It is the single source of truth for task shape and the
// tool's registered name.
package task

import "fmt"

// Status is a task's lifecycle state.
type Status string

const (
	StatusPending    Status = "pending"
	StatusInProgress Status = "in_progress"
	StatusDone       Status = "done"
)

// ToolName is the registered name of the task-declaration tool.
const ToolName = "todo_write"

// Task is one item in the agent's declared plan.
type Task struct {
	Text   string
	Status Status
}

func validStatus(s Status) bool {
	return s == StatusPending || s == StatusInProgress || s == StatusDone
}

// Parse converts the decoded `todos` argument (as delivered in a tool call's
// args or a ToolExecutionStartEvent.Args) into a validated task slice. raw is
// expected to be a []any of map[string]any with non-empty "text" and a valid
// "status".
func Parse(raw any) ([]Task, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("todos must be an array, got %T", raw)
	}
	out := make([]Task, 0, len(items))
	for i, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("todos[%d] must be an object, got %T", i, it)
		}
		text, _ := m["text"].(string)
		if text == "" {
			return nil, fmt.Errorf("todos[%d].text must be a non-empty string", i)
		}
		statusStr, _ := m["status"].(string)
		st := Status(statusStr)
		if !validStatus(st) {
			return nil, fmt.Errorf("todos[%d].status %q invalid (want pending|in_progress|done)", i, statusStr)
		}
		out = append(out, Task{Text: text, Status: st})
	}
	return out, nil
}

// Counts tallies tasks by status.
func Counts(tasks []Task) (done, inProgress, pending int) {
	for _, t := range tasks {
		switch t.Status {
		case StatusDone:
			done++
		case StatusInProgress:
			inProgress++
		case StatusPending:
			pending++
		}
	}
	return
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./pkg/task/ -v`
Expected: PASS（5 个测试全绿）。

- [ ] **Step 5: 建议提交**

```bash
git add pkg/task/task.go pkg/task/task_test.go
git commit -m "feat(task): add shared task-list schema package"
```

---

## Task 2: `todo_write` 工具

**Files:**
- Create: `pkg/tools/builtin/todo.go`
- Test: `pkg/tools/builtin/todo_test.go`
- Modify: `pkg/tools/builtin/init.go`

- [ ] **Step 1: 写失败测试**

Create `pkg/tools/builtin/todo_test.go`:

```go
package builtin

import (
	"context"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/task"
)

func TestTodoWriteDefinition(t *testing.T) {
	def := NewTodoWrite("").Definition()
	if def.Name != task.ToolName {
		t.Fatalf("name = %q, want %q", def.Name, task.ToolName)
	}
	if def.Parameters["type"] != "object" {
		t.Fatalf("parameters must be a JSON schema object: %+v", def.Parameters)
	}
}

func TestTodoWriteExecuteSummary(t *testing.T) {
	tool := NewTodoWrite("")
	args := map[string]any{"todos": []any{
		map[string]any{"text": "a", "status": "done"},
		map[string]any{"text": "b", "status": "in_progress"},
		map[string]any{"text": "c", "status": "pending"},
	}}
	res, err := tool.Execute(context.Background(), "call-1", args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	text := res.Content[0].(interface{ contentPart() })
	_ = text // silence; assert via rendered text below
	got := toolText(res)
	if !strings.Contains(got, "3 tasks") || !strings.Contains(got, "1 done") {
		t.Fatalf("summary wrong: %q", got)
	}
}

func TestTodoWriteExecuteRejectsBad(t *testing.T) {
	tool := NewTodoWrite("")
	args := map[string]any{"todos": []any{
		map[string]any{"text": "a", "status": "nope"},
	}}
	if _, err := tool.Execute(context.Background(), "call-1", args); err == nil {
		t.Fatal("expected error for invalid status")
	}
}

// toolText extracts the concatenated text content of a ToolResult for assertions.
func toolText(res interface {
	GetContent() []interface{}
}) string {
	return ""
}
```

> NOTE: the `toolText` helper above is a placeholder that won't compile — replace it in Step 3 with the real extraction. (Kept separate so Step 2 demonstrably fails first.)

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/tools/builtin/ -run TestTodoWrite -v`
Expected: FAIL — `NewTodoWrite` undefined（且测试文件含占位 helper）。

- [ ] **Step 3: 写实现 + 修正测试 helper**

Create `pkg/tools/builtin/todo.go`:

```go
package builtin

import (
	"context"
	"fmt"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/task"
	"github.com/lcoder/lcoder/pkg/tools"
)

// TodoWrite is a stateless tool the model calls to declare/update its task list.
// It validates the payload and returns a one-line summary; the TUI derives the
// visible task sidebar from the tool call's args (see pkg/tui/tasksidebar.go).
type TodoWrite struct{}

// NewTodoWrite builds the todo_write tool. cwd is unused (no filesystem access).
func NewTodoWrite(cwd string) tools.Executable { return &TodoWrite{} }

func (t *TodoWrite) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name: task.ToolName,
		Description: "Declare or update your task list for a multi-step job. " +
			"Call this when a request needs several steps: list each task with a status. " +
			"Mark a task in_progress before you start it and done when finished. " +
			"Always pass the COMPLETE list every call — it replaces the previous list. " +
			"Skip this for trivial single-step requests.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"todos": map[string]any{
					"type":        "array",
					"description": "The full task list, in order.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"text": map[string]any{
								"type":        "string",
								"description": "Short imperative task description.",
							},
							"status": map[string]any{
								"type": "string",
								"enum": []any{"pending", "in_progress", "done"},
							},
						},
						"required": []any{"text", "status"},
					},
				},
			},
			"required": []any{"todos"},
		},
		ExecutionMode: models.ExecutionSequential,
	}
}

func (t *TodoWrite) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolResult, error) {
	tasks, err := task.Parse(args["todos"])
	if err != nil {
		return models.ToolResult{}, err
	}
	done, inProgress, pending := task.Counts(tasks)
	summary := fmt.Sprintf("Updated %d tasks: %d done, %d in progress, %d pending",
		len(tasks), done, inProgress, pending)
	return models.NewToolResultText(summary), nil
}

var _ tools.Executable = (*TodoWrite)(nil)
```

Now replace the bogus helper in `pkg/tools/builtin/todo_test.go`. Delete the `text := ...` line and the placeholder `toolText` func, and use this real extractor instead:

```go
// toolText extracts the concatenated text content of a ToolResult.
func toolText(res models.ToolResult) string {
	var out string
	for _, part := range res.Content {
		if tc, ok := part.(models.TextContent); ok {
			out += tc.Text
		}
	}
	return out
}
```

And fix `TestTodoWriteExecuteSummary` body to drop the placeholder lines:

```go
func TestTodoWriteExecuteSummary(t *testing.T) {
	tool := NewTodoWrite("")
	args := map[string]any{"todos": []any{
		map[string]any{"text": "a", "status": "done"},
		map[string]any{"text": "b", "status": "in_progress"},
		map[string]any{"text": "c", "status": "pending"},
	}}
	res, err := tool.Execute(context.Background(), "call-1", args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := toolText(res)
	if !strings.Contains(got, "3 tasks") || !strings.Contains(got, "1 done") {
		t.Fatalf("summary wrong: %q", got)
	}
}
```

Add the import `"github.com/lcoder/lcoder/pkg/models"` to the test file's import block.

- [ ] **Step 4: 注册工具**

Modify `pkg/tools/builtin/init.go` — add the `task` import and a registration row:

```go
import (
	"github.com/lcoder/lcoder/pkg/task"
	"github.com/lcoder/lcoder/pkg/tools"
)

func init() {
	for _, f := range []struct {
		name    string
		factory tools.Factory
	}{
		{"read", NewRead},
		{"write", NewWrite},
		{"edit", NewEdit},
		{"bash", NewBash},
		{"ls", NewLs},
		{"grep", NewGrep},
		{"find", NewFind},
		{task.ToolName, NewTodoWrite},
	} {
		tools.DefaultFactories.Register(f.name, f.factory)
	}
}
```

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./pkg/tools/builtin/ -run TestTodoWrite -v`
Expected: PASS（3 个测试全绿）。

- [ ] **Step 6: 建议提交**

```bash
git add pkg/tools/builtin/todo.go pkg/tools/builtin/todo_test.go pkg/tools/builtin/init.go
git commit -m "feat(tools): add todo_write task-declaration tool"
```

---

## Task 3: TUI Model 字段 + 纯 helper

**Files:**
- Create: `pkg/tui/tasksidebar.go`（先放 helper,渲染在 Task 4 加)
- Create: `pkg/tui/tasksidebar_test.go`
- Modify: `pkg/tui/model.go`

- [ ] **Step 1: 写失败测试**

Create `pkg/tui/tasksidebar_test.go`:

```go
package tui

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/task"
)

func sampleTasks() []task.Task {
	return []task.Task{
		{Text: "read auth", Status: task.StatusDone},
		{Text: "split handler", Status: task.StatusInProgress},
	}
}

func TestTaskSidebarVisible(t *testing.T) {
	m := &Model{width: 100, tasks: sampleTasks()}
	if !m.taskSidebarVisible() {
		t.Fatal("sidebar should be visible with tasks on a wide terminal")
	}
	m.taskSidebarHidden = true
	if m.taskSidebarVisible() {
		t.Fatal("sidebar should hide when user toggled it off")
	}
	m.taskSidebarHidden = false
	m.width = 50
	if m.taskSidebarVisible() {
		t.Fatal("sidebar should hide on a narrow terminal")
	}
	m.width = 100
	m.tasks = nil
	if m.taskSidebarVisible() {
		t.Fatal("sidebar should hide with no tasks")
	}
}

func TestMainContentWidth(t *testing.T) {
	m := &Model{width: 100, tasks: sampleTasks()}
	if got := m.mainContentWidth(); got != 100-taskSidebarWidth {
		t.Fatalf("main width with sidebar = %d, want %d", got, 100-taskSidebarWidth)
	}
	m.tasks = nil
	if got := m.mainContentWidth(); got != 100 {
		t.Fatalf("main width without sidebar = %d, want 100", got)
	}
}

func TestApplyTaskUpdate(t *testing.T) {
	m := &Model{}
	ok := m.applyTaskUpdate(map[string]any{"todos": []any{
		map[string]any{"text": "a", "status": "pending"},
	}})
	if !ok || len(m.tasks) != 1 {
		t.Fatalf("valid update failed: ok=%v tasks=%v", ok, m.tasks)
	}
	if ok := m.applyTaskUpdate(map[string]any{"todos": "garbage"}); ok {
		t.Fatal("malformed update should return false")
	}
	if len(m.tasks) != 1 {
		t.Fatal("malformed update must keep the previous task list")
	}
}

func TestTasksFromMessages(t *testing.T) {
	older := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Name: task.ToolName,
		Arguments: map[string]any{"todos": []any{
			map[string]any{"text": "old", "status": "pending"},
		}},
	})
	newer := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Name: task.ToolName,
		Arguments: map[string]any{"todos": []any{
			map[string]any{"text": "new1", "status": "done"},
			map[string]any{"text": "new2", "status": "pending"},
		}},
	})
	got := tasksFromMessages([]models.AgentMessage{older, newer})
	if len(got) != 2 || got[0].Text != "new1" {
		t.Fatalf("should rebuild from the latest todo_write, got %+v", got)
	}
	if tasksFromMessages(nil) != nil {
		t.Fatal("no messages should yield nil")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/tui/ -run 'TestTaskSidebarVisible|TestMainContentWidth|TestApplyTaskUpdate|TestTasksFromMessages' -v`
Expected: FAIL — `taskSidebarWidth` / `taskSidebarVisible` / `applyTaskUpdate` / `tasksFromMessages` undefined。

- [ ] **Step 3: 加 Model 字段**

Modify `pkg/tui/model.go` — add the `task` import:

```go
import (
	"context"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/mcp"
	"github.com/lcoder/lcoder/pkg/skills"
	"github.com/lcoder/lcoder/pkg/task"
)
```

Add these fields to the `Model` struct (place right after the `toolsExpanded bool` line):

```go
	toolsExpanded bool

	// Task sidebar: tasks declared via the todo_write tool, the user's manual
	// hide override, and the cached main-content width set by updateSizes.
	tasks             []task.Task
	taskSidebarHidden bool
	mainWidth         int
```

- [ ] **Step 4: 加纯 helper**

Create `pkg/tui/tasksidebar.go`:

```go
package tui

import (
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/task"
)

// taskSidebarWidth is the fixed outer width (including border) of the task
// sidebar. Below this much terminal width the sidebar is suppressed entirely.
const taskSidebarWidth = 28

// taskSidebarVisible reports whether the sidebar should render: there are tasks,
// the user hasn't hidden it, and the terminal is wide enough to avoid cramping.
func (m *Model) taskSidebarVisible() bool {
	return len(m.tasks) > 0 && !m.taskSidebarHidden && m.width >= 60
}

// mainContentWidth is the width available to the conversation/composer once the
// task sidebar (if visible) has taken its fixed column.
func (m *Model) mainContentWidth() int {
	if m.taskSidebarVisible() {
		return m.width - taskSidebarWidth
	}
	return m.width
}

// applyTaskUpdate parses a todo_write tool's args into the task list. It returns
// true when the list was replaced, false when the args were malformed (the
// previous list is kept).
func (m *Model) applyTaskUpdate(args map[string]any) bool {
	tasks, err := task.Parse(args["todos"])
	if err != nil {
		return false
	}
	m.tasks = tasks
	return true
}

// toggleTaskSidebar flips the user's hide override and re-lays-out.
func (m *Model) toggleTaskSidebar() {
	m.taskSidebarHidden = !m.taskSidebarHidden
	m.updateSizes()
}

// tasksFromMessages rebuilds the task list from history by finding the most
// recent todo_write tool call. Returns nil when none is present.
func tasksFromMessages(msgs []models.AgentMessage) []task.Task {
	for i := len(msgs) - 1; i >= 0; i-- {
		for _, tc := range msgs[i].ToolCalls() {
			if tc.Name == task.ToolName {
				if tasks, err := task.Parse(tc.Arguments["todos"]); err == nil {
					return tasks
				}
			}
		}
	}
	return nil
}
```

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./pkg/tui/ -run 'TestTaskSidebarVisible|TestMainContentWidth|TestApplyTaskUpdate|TestTasksFromMessages' -v`
Expected: PASS（4 个测试全绿）。

- [ ] **Step 6: 建议提交**

```bash
git add pkg/tui/tasksidebar.go pkg/tui/tasksidebar_test.go pkg/tui/model.go
git commit -m "feat(tui): add task-sidebar state and helpers"
```

---

## Task 4: 侧栏渲染

**Files:**
- Modify: `pkg/tui/tasksidebar.go`
- Modify: `pkg/tui/tasksidebar_test.go`

- [ ] **Step 1: 写失败测试**

Add to `pkg/tui/tasksidebar_test.go` (and add `"strings"` to its imports):

```go
func TestRenderTaskSidebar(t *testing.T) {
	out := renderTaskSidebar(sampleTasks(), 20)
	if !strings.Contains(out, "Tasks") {
		t.Fatalf("sidebar missing header: %q", out)
	}
	if !strings.Contains(out, "read auth") || !strings.Contains(out, "split handler") {
		t.Fatalf("sidebar missing task text: %q", out)
	}
	if !strings.Contains(out, "1/2") {
		t.Fatalf("sidebar missing done/total footer: %q", out)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/tui/ -run TestRenderTaskSidebar -v`
Expected: FAIL — `renderTaskSidebar` undefined。

- [ ] **Step 3: 写实现**

Add to `pkg/tui/tasksidebar.go` — extend the import block and append the render funcs:

```go
import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/task"
)
```

```go
// taskGlyph maps a status to its sidebar marker.
func taskGlyph(s task.Status) string {
	switch s {
	case task.StatusDone:
		return styleSuccess().Render("✓")
	case task.StatusInProgress:
		return styleAccent().Render("▸")
	default:
		return styleDim().Render("○")
	}
}

// renderTaskSidebar draws the fixed-width bordered task panel of the given outer
// height. Each task is one line "glyph text"; a "done/total" footer closes it.
func renderTaskSidebar(tasks []task.Task, height int) string {
	inner := taskSidebarWidth - 2 // left/right border columns
	lines := []string{styleAccent().Render("Tasks"), ""}
	for _, t := range tasks {
		text := truncateCells(t.Text, inner-2, "…") // leave room for "glyph "
		lines = append(lines, taskGlyph(t.Status)+" "+text)
	}
	done, _, _ := task.Counts(tasks)
	lines = append(lines, "", styleDim().Render(fmt.Sprintf("%d/%d 完成", done, len(tasks))))

	boxHeight := height - 2 // top/bottom border rows
	if boxHeight < 1 {
		boxHeight = 1
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorFaint).
		Width(inner).
		Height(boxHeight)
	return box.Render(strings.Join(lines, "\n"))
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./pkg/tui/ -run TestRenderTaskSidebar -v`
Expected: PASS。

- [ ] **Step 5: 建议提交**

```bash
git add pkg/tui/tasksidebar.go pkg/tui/tasksidebar_test.go
git commit -m "feat(tui): render task sidebar"
```

---

## Task 5: 布局接线(updateSizes + View)

**Files:**
- Modify: `pkg/tui/model.go:189-201`（`updateSizes`)
- Modify: `pkg/tui/view.go`（`bottomRegion`、`statusLineView`、`View`)

- [ ] **Step 1: 改 `updateSizes` 用主区宽度**

Replace the body of `updateSizes` in `pkg/tui/model.go`:

```go
// updateSizes recomputes layout after a resize, reserving the task sidebar's
// fixed column when it is visible.
func (m *Model) updateSizes() {
	mw := m.mainContentWidth()
	m.mainWidth = mw
	m.input.SetWidth(mw - 2)
	m.input.SyncHeight()
	bottom := m.bottomHeight()
	vh := m.height - bottom
	if vh < 3 {
		vh = 3
	}
	m.viewport.Width = mw
	m.viewport.Height = vh
	m.rebuildViewport()
}
```

- [ ] **Step 2: `bottomRegion` / `statusLineView` 改用 `mainWidth`**

In `pkg/tui/view.go`, inside `bottomRegion`, replace the three `m.width` arguments with `m.mainWidth`:

```go
	if m.menuVisible {
		matches := menuMatches(m.input.Value())
		sections = append(sections, renderMenu(matches, m.menuSelected, m.mainWidth))
	} else if m.fileMenuVisible {
		sections = append(sections, renderFileMenu(m.fileMenuItems, m.fileMenuSelected, m.mainWidth))
	} else if m.cmdPanel.visible {
		sections = append(sections, renderCmdPanel(m.cmdPanel, m.mainWidth))
	}
```

And in `statusLineView`, change the width passed to `statusLine`:

```go
	return statusLine(m.mainWidth, left, m.contextRight())
```

- [ ] **Step 3: `View` 横向拼接侧栏**

Replace the tail of `View` in `pkg/tui/view.go` (the three lines after the overlay switch):

```go
	top := m.viewport.View()
	bottom := m.bottomRegion()
	main := lipgloss.JoinVertical(lipgloss.Left, top, bottom)
	if m.taskSidebarVisible() {
		return lipgloss.JoinHorizontal(lipgloss.Top, main, renderTaskSidebar(m.tasks, m.height))
	}
	return main
```

- [ ] **Step 4: 编译 + 回归测试**

Run: `go build ./... && go test ./pkg/tui/ -v`
Expected: 编译通过;现有 tui 测试 + 新测试全绿。

> NOTE: `mainWidth` 在首个 `WindowSizeMsg`(触发 `updateSizes`)前为 0。这没问题:`stateStartup` 走 `startupView()` 不调用 `bottomRegion`,而离开 startup 的按键 (`handleKey` 的 `stateStartup` 分支) 会调用 `updateSizes`。

- [ ] **Step 5: 建议提交**

```bash
git add pkg/tui/model.go pkg/tui/view.go
git commit -m "feat(tui): lay out task sidebar beside main area"
```

---

## Task 6: 事件接线(handleEvent 拦截 todo_write)

**Files:**
- Modify: `pkg/tui/events.go:47-61`
- Modify: `pkg/tui/tasksidebar_test.go`

- [ ] **Step 1: 写失败测试**

Add to `pkg/tui/tasksidebar_test.go`:

```go
func TestHandleEventTodoWriteUpdatesTasksNoBlock(t *testing.T) {
	m := &Model{width: 100}
	before := len(m.blocks)
	m.handleEvent(events.ToolExecutionStartEvent{
		ToolCallID: "c1",
		ToolName:   task.ToolName,
		Args: map[string]any{"todos": []any{
			map[string]any{"text": "a", "status": "in_progress"},
		}},
	})
	if len(m.tasks) != 1 || m.tasks[0].Status != task.StatusInProgress {
		t.Fatalf("todo_write event should populate tasks, got %+v", m.tasks)
	}
	if len(m.blocks) != before {
		t.Fatalf("todo_write must NOT append a conversation block, blocks grew by %d", len(m.blocks)-before)
	}
}

func TestHandleEventOtherToolStillBlocks(t *testing.T) {
	m := &Model{width: 100}
	m.handleEvent(events.ToolExecutionStartEvent{
		ToolCallID: "c2",
		ToolName:   "bash",
		Args:       map[string]any{"command": "ls"},
	})
	if len(m.blocks) != 1 {
		t.Fatalf("non-todo tool should append a block, got %d blocks", len(m.blocks))
	}
}
```

Add `"github.com/lcoder/lcoder/pkg/events"` to the test file imports.

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/tui/ -run 'TestHandleEventTodoWrite|TestHandleEventOtherTool' -v`
Expected: FAIL — 当前 `todo_write` 会被当普通工具 append 成 block,且 `m.tasks` 不被填充。

> NOTE: `&Model{width:100}` 调用 `handleEvent` 后会执行 `m.updateSizes()`(因为有任务、宽度足够,侧栏可见)。`updateSizes` 调用 `m.input.SetWidth` 与 `m.bottomHeight()`。零值 `InputModel` 的这些方法须不 panic——`bottomHeight` 在 `m.width!=0` 时调用 `bottomRegion`,后者调用 `m.input.View()`。若零值 InputModel 在此 panic,改为在测试里用 `m := &Model{width:100, input: NewInputModel()}`。先按零值跑;若 panic 再加 `NewInputModel()`。

- [ ] **Step 3: 写实现**

In `pkg/tui/events.go`, add the `task` import:

```go
import (
	"fmt"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/mcp"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/task"
)
```

Replace the `ToolExecutionStartEvent` and `ToolExecutionEndEvent` cases:

```go
	case events.ToolExecutionStartEvent:
		// todo_write drives the task sidebar, not a conversation block.
		if e.ToolName == task.ToolName {
			if m.applyTaskUpdate(e.Args) {
				m.updateSizes()
			}
			break
		}
		m.appendBlock(block{
			kind:     blockTool,
			id:       e.ToolCallID,
			toolName: e.ToolName,
			toolArgs: FormatArgs(e.Args),
		})

	case events.ToolExecutionEndEvent:
		if e.ToolName == task.ToolName {
			break
		}
		m.finishTool(e.ToolCallID, e.ToolName, e.Result, e.IsError)
		m.turnTools = append(m.turnTools, toolResultEntry{
			name:    e.ToolName,
			isError: e.IsError,
			content: toolResultText(e.Result),
		})
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./pkg/tui/ -run 'TestHandleEventTodoWrite|TestHandleEventOtherTool' -v`
Expected: PASS。若因零值 InputModel panic,按 Step 2 的 NOTE 给两个测试的 Model 加 `input: NewInputModel()` 后重跑。

- [ ] **Step 5: 建议提交**

```bash
git add pkg/tui/events.go pkg/tui/tasksidebar_test.go
git commit -m "feat(tui): drive task sidebar from todo_write events"
```

---

## Task 7: 键位与命令(Ctrl+T + /tasks)

**Files:**
- Modify: `pkg/tui/keys.go`（`handleInputKey`、`handleProcessingKey`、`dispatchSlash`)
- Modify: `pkg/tui/menu.go`（命令注册)

- [ ] **Step 1: `handleInputKey` 加 Ctrl+T**

In `pkg/tui/keys.go`, at the very top of `handleInputKey` (before the cmdPanel block):

```go
func (m *Model) handleInputKey(k tea.KeyMsg) (*Model, tea.Cmd) {
	if k.Type == tea.KeyCtrlT {
		m.toggleTaskSidebar()
		return m, nil
	}

	// Command panel (ephemeral output) intercepts keys while visible.
	if m.cmdPanel.visible {
```

- [ ] **Step 2: `handleProcessingKey` 加 Ctrl+T**

In the `switch k.Type` of `handleProcessingKey`, add a case alongside `tea.KeyCtrlO`:

```go
	case tea.KeyCtrlO:
		m.toolsExpanded = !m.toolsExpanded
		m.rebuildViewport()
		return m, nil
	case tea.KeyCtrlT:
		m.toggleTaskSidebar()
		return m, nil
```

- [ ] **Step 3: `dispatchSlash` 加 /tasks**

In the `switch entry.Name` of `dispatchSlash`, add a case next to `"tools"`:

```go
	case "tools":
		m.toolsExpanded = !m.toolsExpanded
		m.rebuildViewport()
	case "tasks":
		m.toggleTaskSidebar()
```

- [ ] **Step 4: 注册 /tasks 命令条目**

In `pkg/tui/menu.go`, add to `commandRegistry` right after the `tools` entry:

```go
	{Name: "tools", Description: "Toggle detailed tool & thinking view (Ctrl+O)", Category: "View"},
	{Name: "tasks", Description: "Toggle task sidebar (Ctrl+T)", Category: "View"},
```

- [ ] **Step 5: 编译 + 回归**

Run: `go build ./... && go test ./pkg/tui/ -v`
Expected: 编译通过,测试全绿。

- [ ] **Step 6: 建议提交**

```bash
git add pkg/tui/keys.go pkg/tui/menu.go
git commit -m "feat(tui): toggle task sidebar via Ctrl+T and /tasks"
```

---

## Task 8: Resume(从历史重建任务)

**Files:**
- Modify: `pkg/tui/keys.go:631-640`（`loadSession`)

> `tasksFromMessages` 已在 Task 3 实现并测过。本任务只接线。

- [ ] **Step 1: 在 `loadSession` 重建任务并重排布局**

Replace `loadSession` in `pkg/tui/keys.go`:

```go
// loadSession replaces history with a stored session's messages and rebuilds the
// task sidebar from the latest todo_write call in that history.
func (m *Model) loadSession(sess *session.Session) {
	if sess == nil {
		return
	}
	msgs := sess.ActiveMessages()
	m.blocks = blocksFromMessages(msgs)
	m.tasks = tasksFromMessages(msgs)
	m.agent.SetMessages(msgs)
	m.updateSizes()
}
```

- [ ] **Step 2: 编译 + 回归**

Run: `go build ./... && go test ./pkg/tui/ -v`
Expected: 编译通过,测试全绿。

- [ ] **Step 3: 建议提交**

```bash
git add pkg/tui/keys.go
git commit -m "feat(tui): rebuild task sidebar on session resume"
```

---

## Task 9: 全量验证 + 冒烟

**Files:** 无（仅验证)

- [ ] **Step 1: 全量构建与测试**

Run:
```bash
go build ./...
go test ./...
```
Expected: 全部通过(集成测试在无凭证时优雅 skip,不影响结果)。

- [ ] **Step 2: go vet**

Run: `go vet ./...`
Expected: 无新增告警(忽略仓库既有的 info 级提示)。

- [ ] **Step 3: 手动冒烟(可选,需真实 provider)**

启动 TUI,给一个多步任务(如「重构 X:先读再改再测」),确认:
- 模型调用 `todo_write` 后右侧栏出现,任务行带 `○/▸/✓` 字形与 `N/M 完成` 底栏。
- `todo_write` 不在对话流里留 tool block。
- `Ctrl+T` 能隐藏/恢复侧栏;`/tasks` 同效。
- 窗口缩到 < 60 列时侧栏自动消失,主区恢复全宽。
- `/sessions` 切到含任务的历史会话后,侧栏按最后一次 `todo_write` 重建。

- [ ] **Step 4: 建议提交(若冒烟有微调)**

```bash
git add -A
git commit -m "chore(tui): task system smoke-test fixes"
```

---

## Self-Review

**Spec coverage:**
- 任务作为工具、LLM 驱动 → Task 2(`todo_write`)。
- 单工具整体替换 → Task 1 `Parse` 全量解析 + Task 6 全量覆盖 `m.tasks`。
- 三态 pending/in_progress/done → Task 1 状态常量 + 校验。
- 内存态 + 历史重建 → Task 3 `tasksFromMessages` + Task 8 `loadSession` 接线;无独立存储层。
- 自动显隐 + Ctrl+T → Task 3 `taskSidebarVisible`(含宽度<60 自动隐藏)+ Task 7 键位/命令。
- 右侧栏渲染(字形 + N/M)→ Task 4。
- 不新增事件类型、不给工具塞 bus → Task 6 复用 `ToolExecutionStartEvent`。
- 数据流(工具校验返回摘要、TUI 从 Args 派生)→ Task 2 + Task 6。
- 错误处理:工具校验失败返回 error → Task 2;TUI 解析失败保留旧值 → Task 3 `applyTaskUpdate` 返回 false;宽度<60 隐藏 → Task 3。
- 测试覆盖 → 每个 Task 自带 TDD 步骤。

**Placeholder scan:** Task 2 Step 1 故意放了一个会编译失败的占位 helper 以演示「先失败」,并在 Step 3 明确给出替换后的真实代码——这是有意的 red→green,不是遗留占位。其余无 TBD/TODO。

**Type consistency:** `task.Parse(raw any) ([]Task, error)`、`task.Counts(...) (int,int,int)`、`task.ToolName`、`task.Status` 常量、`Model.tasks []task.Task`、`taskSidebarWidth` 常量、`renderTaskSidebar(tasks, height)`、`applyTaskUpdate(args) bool`、`tasksFromMessages(msgs) []task.Task`、`taskSidebarVisible()`/`mainContentWidth()`/`toggleTaskSidebar()` 在各任务间签名一致。`models.ToolCallContent.Arguments`、`models.TextContent`、`models.NewToolResultText`、`models.ExecutionSequential`、`events.ToolExecutionStartEvent.Args`/`.ToolName` 均已对照真实源码核实。
