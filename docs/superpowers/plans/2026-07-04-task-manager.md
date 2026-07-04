# TaskManager 独立组件实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 task 管理从消息历史解析重构为独立的 `TaskManager` 组件，解决上下文压缩丢失、崩溃恢复不可靠、模型覆盖偏向三个核心问题。

**Architecture:** 新增 `pkg/task.TaskManager` 持有当前任务列表，提供 `ReplaceAll`（带 reconciliation）、`Snapshot`/`Restore`、事件订阅。`Agent` 构造时创建 `TaskManager`，`executor` 在处理 `todo_write` 时调用它，`checkpoint` 序列化其状态，TUI 通过独立事件同步。任务提醒由 `TaskManager.FormatReminder` 生成并作为 ephemeral reminder 注入。

**Tech Stack:** Go 1.25.4，现有 `pkg/task`、`pkg/agent`、`pkg/events`、`pkg/checkpoint`、`pkg/tui`。

---

## 文件结构

| 文件 | 职责 |
| --- | --- |
| `pkg/task/manager.go`（新） | `TaskManager` 核心：ReplaceAll、List、Counts、FormatReminder、Subscribe、Snapshot、Restore |
| `pkg/task/manager_test.go`（新） | `TaskManager` 单元测试 |
| `pkg/task/state.go`（新） | 可序列化的 `ManagerState` |
| `pkg/events/task.go`（新） | `TaskListUpdatedEvent` 定义 |
| `pkg/checkpoint/checkpoint.go`（改） | `RuntimeSnapshot` 新增 `TaskManagerState` 字段 |
| `pkg/agent/loop.go`（改） | `Agent` 结构体新增 `taskMgr`，`refreshEphemeralReminders` 使用 `TaskManager` |
| `pkg/agent/checkpoint.go`（改） | `CheckpointWithReason`/`Restore` 保存和恢复 `TaskManagerState` |
| `pkg/agent/builder.go`（改） | `AgentBuilder` 创建 `TaskManager` 并注入 `executor` |
| `pkg/agent/executor.go`（改） | `executor` 处理 `todo_write` 时调用 `TaskManager.ReplaceAll` |
| `pkg/agent/state_snapshot.go`（改，可选） | 若需要，把 `taskMgr` 的快照纳入 `RuntimeState`（本计划不采用，直接用 checkpoint 字段） |
| `pkg/agent/reminders.go`（改） | 移除/弃用 `UnresolvedTodosReminder`，或改为调用 `TaskManager` |
| `pkg/tui/events.go`（改） | 处理 `TaskListUpdatedEvent` |
| `pkg/tui/model.go`（改） | 启动时从 agent 读取初始 task 列表 |
| `cmd/lcoder/main.go`（改） | TUI 启动时把 agent 的 task manager 初始状态传给 TUI（或 TUI 自己读取） |

---

## 前置依赖

确保已阅读：
- `docs/superpowers/specs/2026-07-04-task-manager-design.md`
- `pkg/task/task.go`
- `pkg/tools/builtin/todo.go`
- `pkg/agent/loop.go`
- `pkg/agent/executor.go`
- `pkg/agent/checkpoint.go`
- `pkg/checkpoint/checkpoint.go`
- `pkg/tui/events.go`
- `pkg/tui/model.go`

---

## Task 1: 实现 `pkg/task.TaskManager` 核心

**Files:**
- Create: `pkg/task/manager.go`
- Create: `pkg/task/manager_test.go`
- Create: `pkg/task/state.go`

### Step 1.1: 编写 `pkg/task/state.go`

```go
package task

// ManagerState is a serializable snapshot of a TaskManager.
type ManagerState struct {
    Tasks []Task `json:"tasks"`
}
```

### Step 1.2: 编写 `pkg/task/manager.go`

```go
package task

import (
    "fmt"
    "sync"
)

// Manager holds the agent's current task list and notifies subscribers on change.
type Manager struct {
    mu    sync.RWMutex
    tasks []Task
    subs  []func([]Task)
}

// NewManager creates an empty task manager.
func NewManager() *Manager {
    return &Manager{}
}

// List returns a snapshot of the current tasks.
func (m *Manager) List() []Task {
    m.mu.RLock()
    defer m.mu.RUnlock()
    out := make([]Task, len(m.tasks))
    copy(out, m.tasks)
    return out
}

// Counts tallies tasks by status.
func (m *Manager) Counts() (done, inProgress, pending int) {
    m.mu.RLock()
    defer m.mu.RUnlock()
    return Counts(m.tasks)
}

// FormatReminder returns a reminder string when there are unfinished tasks.
// It returns an empty string when there is no task or all are done.
func (m *Manager) FormatReminder() string {
    m.mu.RLock()
    tasks := make([]Task, len(m.tasks))
    copy(tasks, m.tasks)
    m.mu.RUnlock()

    if len(tasks) == 0 {
        return ""
    }
    done, inProgress, pending := Counts(tasks)
    remaining := inProgress + pending
    if remaining == 0 {
        return ""
    }
    return fmt.Sprintf("You have %d unfinished todo item(s) (%d done). Continue working toward them; do not stop until they are complete or you report a blocker.", remaining, done)
}

// Subscribe registers a callback that receives a snapshot of tasks after each change.
// The callback is invoked without holding the lock.
func (m *Manager) Subscribe(fn func([]Task)) {
    m.mu.Lock()
    defer m.mu.Unlock()
    m.subs = append(m.subs, fn)
}

func (m *Manager) notify() {
    m.mu.RLock()
    snapshot := make([]Task, len(m.tasks))
    copy(snapshot, m.tasks)
    subs := make([]func([]Task), len(m.subs))
    copy(subs, m.subs)
    m.mu.RUnlock()

    for _, fn := range subs {
        fn(snapshot)
    }
}

// Snapshot returns a serializable copy of the manager state.
func (m *Manager) Snapshot() ManagerState {
    m.mu.RLock()
    defer m.mu.RUnlock()
    tasks := make([]Task, len(m.tasks))
    copy(tasks, m.tasks)
    return ManagerState{Tasks: tasks}
}

// Restore replaces the manager state from a snapshot.
func (m *Manager) Restore(state ManagerState) error {
    for i, t := range state.Tasks {
        if t.Text == "" {
            return fmt.Errorf("task %d: text must not be empty", i)
        }
        if !validStatus(t.Status) {
            return fmt.Errorf("task %d: invalid status %q", i, t.Status)
        }
    }
    m.mu.Lock()
    m.tasks = make([]Task, len(state.Tasks))
    copy(m.tasks, state.Tasks)
    m.mu.Unlock()
    m.notify()
    return nil
}

// ReplaceAll replaces the current task list with the provided one.
// It reconciles against the old list: pending/in_progress tasks that are missing
// from the new list are automatically re-added, and warnings are returned.
// Completed tasks may be dropped.
func (m *Manager) ReplaceAll(tasks []Task) (reconciled []Task, warnings []string, err error) {
    for i, t := range tasks {
        if t.Text == "" {
            return nil, nil, fmt.Errorf("task %d: text must not be empty", i)
        }
        if !validStatus(t.Status) {
            return nil, nil, fmt.Errorf("task %d: invalid status %q", i, t.Status)
        }
    }

    m.mu.Lock()
    oldTasks := m.tasks
    m.mu.Unlock()

    oldIndex := make(map[string]Task, len(oldTasks))
    for _, t := range oldTasks {
        oldIndex[t.Text] = t
    }

    newIndex := make(map[string]struct{}, len(tasks))
    reconciled = make([]Task, 0, len(tasks))
    for _, t := range tasks {
        newIndex[t.Text] = struct{}{}
        reconciled = append(reconciled, t)
    }

    for _, old := range oldTasks {
        if old.Status == StatusDone {
            continue
        }
        if _, ok := newIndex[old.Text]; !ok {
            reconciled = append(reconciled, old)
            warnings = append(warnings, fmt.Sprintf("re-added unfinished task: %q", old.Text))
        }
    }

    m.mu.Lock()
    m.tasks = reconciled
    m.mu.Unlock()
    m.notify()
    return reconciled, warnings, nil
}
```

### Step 1.3: 编写 `pkg/task/manager_test.go`

```go
package task

import (
    "testing"
)

func TestManagerReplaceAll(t *testing.T) {
    tm := NewManager()

    // initial list
    reconciled, warnings, err := tm.ReplaceAll([]Task{
        {Text: "a", Status: StatusPending},
        {Text: "b", Status: StatusInProgress},
    })
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if len(warnings) != 0 {
        t.Fatalf("expected no warnings, got %v", warnings)
    }
    if len(reconciled) != 2 {
        t.Fatalf("expected 2 tasks, got %d", len(reconciled))
    }
}

func TestManagerReplaceAllReconcilesMissingUnfinished(t *testing.T) {
    tm := NewManager()
    _, _, _ = tm.ReplaceAll([]Task{
        {Text: "old1", Status: StatusPending},
        {Text: "old2", Status: StatusInProgress},
        {Text: "old3", Status: StatusDone},
    })

    reconciled, warnings, err := tm.ReplaceAll([]Task{
        {Text: "new", Status: StatusPending},
    })
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if len(warnings) != 2 {
        t.Fatalf("expected 2 warnings for old1 and old2, got %v", warnings)
    }
    if len(reconciled) != 3 {
        t.Fatalf("expected 3 tasks (new + old1 + old2), got %d", len(reconciled))
    }

    seen := make(map[string]bool)
    for _, t := range reconciled {
        seen[t.Text] = true
    }
    if !seen["old1"] || !seen["old2"] || !seen["new"] {
        t.Fatalf("missing expected tasks: %+v", reconciled)
    }
    if seen["old3"] {
        t.Fatalf("done task old3 should have been dropped")
    }
}

func TestManagerReplaceAllStatusProgression(t *testing.T) {
    tm := NewManager()
    _, _, _ = tm.ReplaceAll([]Task{
        {Text: "x", Status: StatusPending},
    })

    reconciled, _, err := tm.ReplaceAll([]Task{
        {Text: "x", Status: StatusDone},
    })
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if len(reconciled) != 1 || reconciled[0].Status != StatusDone {
        t.Fatalf("expected x done, got %+v", reconciled)
    }
}

func TestManagerReplaceAllInvalid(t *testing.T) {
    tm := NewManager()
    _, _, err := tm.ReplaceAll([]Task{
        {Text: "", Status: StatusPending},
    })
    if err == nil {
        t.Fatal("expected error for empty text")
    }

    _, _, err = tm.ReplaceAll([]Task{
        {Text: "x", Status: "bogus"},
    })
    if err == nil {
        t.Fatal("expected error for invalid status")
    }
}

func TestManagerFormatReminder(t *testing.T) {
    tm := NewManager()
    if r := tm.FormatReminder(); r != "" {
        t.Fatalf("expected empty reminder for no tasks, got %q", r)
    }

    _, _, _ = tm.ReplaceAll([]Task{
        {Text: "a", Status: StatusDone},
        {Text: "b", Status: StatusPending},
    })
    r := tm.FormatReminder()
    if r == "" {
        t.Fatal("expected non-empty reminder for unfinished task")
    }

    _, _, _ = tm.ReplaceAll([]Task{
        {Text: "a", Status: StatusDone},
    })
    if r := tm.FormatReminder(); r != "" {
        t.Fatalf("expected empty reminder when all done, got %q", r)
    }
}

func TestManagerSubscribe(t *testing.T) {
    tm := NewManager()
    var called []Task
    tm.Subscribe(func(ts []Task) {
        called = make([]Task, len(ts))
        copy(called, ts)
    })

    _, _, _ = tm.ReplaceAll([]Task{{Text: "x", Status: StatusPending}})
    if len(called) != 1 || called[0].Text != "x" {
        t.Fatalf("subscriber did not receive snapshot: %+v", called)
    }
}

func TestManagerSnapshotRestore(t *testing.T) {
    tm := NewManager()
    _, _, _ = tm.ReplaceAll([]Task{
        {Text: "a", Status: StatusDone},
        {Text: "b", Status: StatusInProgress},
    })

    snap := tm.Snapshot()
    tm2 := NewManager()
    if err := tm2.Restore(snap); err != nil {
        t.Fatalf("restore failed: %v", err)
    }
    if got := tm2.List(); len(got) != 2 {
        t.Fatalf("expected 2 tasks after restore, got %d", len(got))
    }
}
```

### Step 1.4: 运行测试

```bash
cd D:\code_practise\project\lab_pj\Lcoder
go test ./pkg/task -v
```

Expected: all tests PASS.

### Step 1.5: 提交

```bash
cd D:\code_practise\project\lab_pj\Lcoder
git add pkg/task/manager.go pkg/task/manager_test.go pkg/task/state.go
git commit -m "feat(task): add TaskManager with reconciliation and snapshot/restore"
```

---

## Task 2: 定义 `TaskListUpdatedEvent`

**Files:**
- Create: `pkg/events/task.go`

### Step 2.1: 编写事件定义

```go
package events

import "github.com/lcoder/lcoder/pkg/task"

// TaskListUpdated is emitted when the agent's task list changes.
const TaskListUpdated EventType = "task_list_updated"

// TaskListUpdatedEvent carries the new task list.
type TaskListUpdatedEvent struct {
    Base
    Tasks []task.Task `json:"tasks"`
}
```

### Step 2.2: 编译检查

```bash
cd D:\code_practise\project\lab_pj\Lcoder
go build ./pkg/events
```

Expected: build succeeds.

### Step 2.3: 提交

```bash
cd D:\code_practise\project\lab_pj\Lcoder
git add pkg/events/task.go
git commit -m "feat(events): add TaskListUpdatedEvent"
```

---

## Task 3: 扩展 Checkpoint 以保存 TaskManager 状态

**Files:**
- Modify: `pkg/checkpoint/checkpoint.go`

### Step 3.1: 读取现有 `RuntimeSnapshot`

确认 `pkg/checkpoint/checkpoint.go` 中 `RuntimeSnapshot` 的当前字段。

### Step 3.2: 添加 `TaskManagerState` 字段

在 `RuntimeSnapshot` 结构体中添加：

```go
import "github.com/lcoder/lcoder/pkg/task"

// ... existing fields ...

// TaskManagerState holds the runtime task list snapshot.
TaskManagerState *task.ManagerState `json:"task_manager_state,omitempty"`
```

示例（假设现有结构）：

```go
type RuntimeSnapshot struct {
    State            int                `json:"state"`
    Turn             int                `json:"turn"`
    IsAtTurnBoundary bool               `json:"is_at_turn_boundary"`
    SteeringQueue    []models.AgentMessage `json:"steering_queue,omitempty"`
    FollowUpQueue    []models.AgentMessage `json:"follow_up_queue,omitempty"`
    ActiveDeferred   map[string]bool    `json:"active_deferred,omitempty"`
    TaskManagerState *task.ManagerState `json:"task_manager_state,omitempty"`
}
```

### Step 3.3: 编译检查

```bash
cd D:\code_practise\project\lab_pj\Lcoder
go build ./pkg/checkpoint
```

Expected: build succeeds.

### Step 3.4: 提交

```bash
cd D:\code_practise\project\lab_pj\Lcoder
git add pkg/checkpoint/checkpoint.go
git commit -m "feat(checkpoint): add TaskManagerState to RuntimeSnapshot"
```

---

## Task 4: 在 Agent 中创建 TaskManager 并接入 Checkpoint

**Files:**
- Modify: `pkg/agent/loop.go`
- Modify: `pkg/agent/checkpoint.go`

### Step 4.1: 修改 `pkg/agent/loop.go` 中 `Agent` 结构体

在 `Agent` 结构体中添加：

```go
type Agent struct {
    cfg          Config
    mgr          *contextmgr.Manager
    llm          *llm.Client
    registry     *tools.Registry
    bus          *events.Bus
    obsCollector *observability.Collector
    emitter      *eventEmitter

    loopState *stateHolder
    streamer  *streamer
    executor  *executor
    taskMgr   *task.Manager  // NEW
}
```

### Step 4.2: 修改 `New` 函数

在 `New` 中创建 `TaskManager`：

```go
func New(cfg Config, llmClient *llm.Client, registry *tools.Registry, perms *permissions.Engine, bus *events.Bus) *Agent {
    // ... existing setup ...
    ag := &Agent{
        cfg:      cfg,
        mgr:      mgr,
        llm:      llmClient,
        registry: registry,
        bus:      bus,
    }
    ag.emitter = &eventEmitter{bus: bus}
    ag.loopState = newStateHolder()
    ag.taskMgr = task.NewManager()  // NEW
    ag.streamer = &streamer{cfg: &ag.cfg, llm: ag.llm, mgr: ag.mgr, emitter: ag.emitter}
    ag.executor = &executor{cfg: &ag.cfg, mgr: ag.mgr, registry: ag.registry, permissions: perms, emitter: ag.emitter}
    return ag
}
```

### Step 4.3: 暴露 `TaskManager` 访问方法

在 `pkg/agent/loop.go` 中添加：

```go
// TaskManager returns the agent's task manager.
func (a *Agent) TaskManager() *task.Manager {
    return a.taskMgr
}
```

### Step 4.4: 修改 `pkg/agent/checkpoint.go` 的 `CheckpointWithReason`

在构造 `RuntimeSnapshot` 时加入 task manager 快照：

```go
Runtime: &checkpoint.RuntimeSnapshot{
    State:            int(stateSnap.State),
    Turn:             stateSnap.Turn,
    IsAtTurnBoundary: stateSnap.State == StateIdle,
    SteeringQueue:    stateSnap.SteeringQueue,
    FollowUpQueue:    stateSnap.FollowUpQueue,
    ActiveDeferred:   execSnap.ActiveDeferred,
    TaskManagerState: ptr(a.taskMgr.Snapshot()),  // NEW
},
```

其中 `ptr` 是一个本地辅助函数：

```go
func ptr[T any](v T) *T { return &v }
```

或者直接在调用处内联：`&a.taskMgr.Snapshot()`。

### Step 4.5: 修改 `pkg/agent/checkpoint.go` 的 `Restore`

在恢复 `executor` 之后添加 task manager 恢复逻辑：

```go
if cp.Runtime.TaskManagerState != nil {
    if err := a.taskMgr.Restore(*cp.Runtime.TaskManagerState); err != nil {
        return fmt.Errorf("agent: restore task manager: %w", err)
    }
} else {
    // Fallback: rebuild from message history for backwards compatibility.
    if tasks := latestTodos(a.mgr.AllMessages()); len(tasks) > 0 {
        _, _, _ = a.taskMgr.ReplaceAll(tasks)
    }
}
```

> 注意：`latestTodos` 原本在 `pkg/agent/reminders.go` 中。由于 Task 6 会移除该文件，需要把 `latestTodos` 作为内部辅助函数保留在 `pkg/agent/checkpoint.go` 或 `pkg/agent/loop.go` 中（放在 package 级别，不导出），供 `Restore` 的降级路径使用。

### Step 4.6: 编译检查

```bash
cd D:\code_practise\project\lab_pj\Lcoder
go build ./pkg/agent
```

Expected: build succeeds.

### Step 4.7: 提交

```bash
cd D:\code_practise\project\lab_pj\Lcoder
git add pkg/agent/loop.go pkg/agent/checkpoint.go
git commit -m "feat(agent): create TaskManager and wire into checkpoint save/restore"
```

---

## Task 5: 将 TaskManager 注入 executor 并处理 `todo_write`

**Files:**
- Modify: `pkg/agent/executor.go`
- Modify: `pkg/agent/builder.go`

### Step 5.1: 读取现有 `executor` 结构

确认 `pkg/agent/executor.go` 中 `executor` 结构体定义和构造方式。

### Step 5.2: 修改 `executor` 结构体

添加 `taskMgr` 字段：

```go
type executor struct {
    cfg         *Config
    mgr         *contextmgr.Manager
    registry    *tools.Registry
    permissions *permissions.Engine
    emitter     *eventEmitter
    obs         *observability.Collector

    mu             sync.Mutex
    activeDeferred map[string]bool
    taskMgr        *task.Manager  // NEW
}
```

### Step 5.3: 修改 `executor` 构造函数

如果 `executor` 是直接在 `New` 中构造的，改为：

```go
func newExecutor(cfg *Config, mgr *contextmgr.Manager, registry *tools.Registry, perms *permissions.Engine, emitter *eventEmitter, taskMgr *task.Manager) *executor {
    return &executor{
        cfg:            cfg,
        mgr:            mgr,
        registry:       registry,
        permissions:    perms,
        emitter:        emitter,
        taskMgr:        taskMgr,
        activeDeferred: make(map[string]bool),
    }
}
```

如果 `executor` 是直接 `&executor{...}` 字面量构造的，直接添加 `taskMgr` 字段赋值。

### Step 5.4: 修改 `pkg/agent/builder.go` 中的构造

找到 `AgentBuilder.Build` 中创建 `executor` 的位置，传入 `ag.taskMgr`：

```go
ag.executor = newExecutor(&ag.cfg, ag.mgr, ag.registry, perms, ag.emitter, ag.taskMgr)
```

或者在 `New` 中：

```go
ag.executor = &executor{
    cfg:            &ag.cfg,
    mgr:            ag.mgr,
    registry:       ag.registry,
    permissions:    perms,
    emitter:        ag.emitter,
    taskMgr:        ag.taskMgr,
    activeDeferred: make(map[string]bool),
}
```

### Step 5.5: 在 `executor.execute` 或工具执行路径中处理 `todo_write`

在 `executor` 执行工具调用时，对 `todo_write` 做特殊处理。假设现有代码在 `executeTool` 或类似函数中调用 `registry.Execute` 并获取结果。

在调用工具后、返回结果前，添加：

```go
import "github.com/lcoder/lcoder/pkg/task"

// ... inside tool execution path ...
if tc.Name == task.ToolName {
    rawTasks, ok := tc.Arguments["todos"]
    if ok {
        if parsed, err := task.Parse(rawTasks); err == nil {
            reconciled, warnings, err := e.taskMgr.ReplaceAll(parsed)
            // Include warnings in the result text so the LLM sees them.
            resultText := resultTextFromExecution(res)
            if len(warnings) > 0 {
                resultText += "\n\nWarnings:\n"
                for _, w := range warnings {
                    resultText += "- " + w + "\n"
                }
            }
            // rebuild res with the updated text
            res = models.NewToolExecutionResultText(resultText)
            _ = reconciled
            _ = err
        }
    }
}
```

> 注意：需要把 `res` 是 `models.ToolExecutionResult` 的文本内容取出来。如果 `res.Content` 是 `[]models.ContentPart`，遍历找到 `models.TextContent` 并追加文本。

更干净的做法是修改 `todo_write` 工具本身的 `Execute` 方法让它接受 `TaskManager`。但由于 `tools.Executable` 接口没有传入 `TaskManager` 的位置，所以在 `executor` 层处理更合适。

如果当前 `executor.execute` 的返回值已经处理好了结果文本，也可以在 emit `ToolExecutionEndEvent` 之前调整 `result`。

### Step 5.6: 从 `TaskManager` 订阅转发事件到事件总线

在 `New` 中（或在 `executor` 构造后）注册订阅：

```go
ag.taskMgr.Subscribe(func(tasks []task.Task) {
    ag.emit(context.Background(), events.TaskListUpdatedEvent{
        Base:  events.Base{Type: events.TaskListUpdated, Turn: ag.loopState.Turn()},
        Tasks: tasks,
    })
})
```

> 注意：`ag.emit` 要求 `context.Context`。这里用 `context.Background()` 并不理想，但在构造时没有运行 context。更好的方式是让 `TaskManager` 的订阅回调在 `ReplaceAll` 时接收 context，或者让 `executor` 在工具执行路径中显式 emit 事件（它持有运行 context）。
>
> **推荐做法**：不在构造时注册全局订阅，而是在 `executor` 处理 `todo_write` 的运行 context 中直接 emit 事件。这样事件带有正确的 turn 信息，也不需要 `context.Background()`。

因此，Step 5.5 的代码应改为：

```go
if tc.Name == task.ToolName {
    rawTasks, ok := tc.Arguments["todos"]
    if ok {
        if parsed, err := task.Parse(rawTasks); err == nil {
            reconciled, warnings, _ := e.taskMgr.ReplaceAll(parsed)
            resultText := resultTextFromExecution(res)
            if len(warnings) > 0 {
                resultText += "\n\nWarnings:\n"
                for _, w := range warnings {
                    resultText += "- " + w + "\n"
                }
            }
            res = models.NewToolExecutionResultText(resultText)

            // Emit event with correct turn context.
            e.emitter.emit(ctx, events.TaskListUpdatedEvent{
                Base:  events.Base{Type: events.TaskListUpdated, Turn: turn},
                Tasks: reconciled,
            })
        }
    }
}
```

其中 `turn` 是当前 turn 编号，由 `execute` 方法参数提供。

### Step 5.7: 编译检查

```bash
cd D:\code_practise\project\lab_pj\Lcoder
go build ./pkg/agent
```

Expected: build succeeds.

### Step 5.8: 提交

```bash
cd D:\code_practise\project\lab_pj\Lcoder
git add pkg/agent/executor.go pkg/agent/builder.go pkg/agent/loop.go
git commit -m "feat(agent): executor reconciles todo_write through TaskManager"
```

---

## Task 6: 用 `TaskManager` 生成 Ephemeral Reminder

**Files:**
- Modify: `pkg/agent/loop.go`
- Modify: `pkg/agent/reminders.go`

### Step 6.1: 修改 `refreshEphemeralReminders`

在 `pkg/agent/loop.go` 中：

```go
// refreshEphemeralReminders runs every producer over the current conversation
// and stages the results on the context manager for this turn only.
func (a *Agent) refreshEphemeralReminders() {
    a.mgr.ClearEphemeralReminders()

    var reminders []string
    if r := a.taskMgr.FormatReminder(); r != "" {
        reminders = append(reminders, r)
    }

    if len(a.cfg.ReminderProducers) > 0 {
        msgs := a.mgr.AllMessages()
        for _, p := range a.cfg.ReminderProducers {
            reminders = append(reminders, p(msgs)...)
        }
    }

    if len(reminders) > 0 {
        a.mgr.SetEphemeralReminders(reminders)
    }
}
```

### Step 6.2: 移除或改写 `pkg/agent/reminders.go`

`UnresolvedTodosReminder` 现在由 `TaskManager.FormatReminder` 替代。

选项 A（完全移除）：
- 把 `latestTodos` 函数复制到 `pkg/agent/checkpoint.go` 或 `pkg/agent/loop.go` 作为未导出辅助函数（供 `Restore` 降级路径使用）。
- 删除 `pkg/agent/reminders.go` 和 `pkg/agent/reminders_test.go`。
- 从 `cmd/lcoder/main.go` 中移除 `agent.WithReminderProducer(agent.UnresolvedTodosReminder)`。

选项 B（保留为兼容包装）：
- 把 `UnresolvedTodosReminder` 改为接收 `*task.Manager` 的辅助函数。

推荐选项 A，因为 `TaskManager` 已完全覆盖其职责。

### Step 6.3: 修改 `cmd/lcoder/main.go`

找到：

```go
WithReminderProducer(agent.UnresolvedTodosReminder).
```

删除这一行（或注释掉）。`TaskManager` 会自动生成任务提醒。

### Step 6.4: 编译检查

```bash
cd D:\code_practise\project\lab_pj\Lcoder
go build ./...
go test ./pkg/agent -run TestAgentCheckpointRoundTrip -v
```

Expected: build succeeds, checkpoint roundtrip test passes.

### Step 6.5: 提交

```bash
cd D:\code_practise\project\lab_pj\Lcoder
git add pkg/agent/loop.go pkg/agent/reminders.go pkg/agent/reminders_test.go cmd/lcoder/main.go
git commit -m "feat(agent): generate task reminders from TaskManager"
```

---

## Task 7: TUI 通过事件同步 TaskManager

**Files:**
- Modify: `pkg/tui/events.go`
- Modify: `pkg/tui/model.go`

### Step 7.1: 修改 `pkg/tui/events.go` 的 `handleEvent`

添加对 `events.TaskListUpdatedEvent` 的处理：

```go
case events.TaskListUpdatedEvent:
    m.tasks = e.Tasks
    m.updateSizes()
```

同时可以选择保留或移除 `ToolExecutionStartEvent` 中对 `todo_write` 的特殊处理：

```go
case events.ToolExecutionStartEvent:
    // todo_write is now handled by TaskListUpdatedEvent.
    // Keep this branch only if you want to show todo_write as a tool block too.
    // if e.ToolName == task.ToolName {
    //     break
    // }
    m.appendBlock(block{
        kind:     blockTool,
        id:       e.ToolCallID,
        toolName: e.ToolName,
        toolArgs: FormatArgs(e.Args),
    })
```

推荐：完全移除 `todo_write` 的特殊分支，让 `TaskListUpdatedEvent` 统一处理。

### Step 7.2: 修改 `pkg/tui/model.go` 的 `NewModel`

在从 `ag.AllMessages()` 恢复 `m.tasks` 的代码附近，改为优先从 agent 的 `TaskManager` 读取：

```go
if ag.TaskManager() != nil {
    m.tasks = ag.TaskManager().List()
} else if msgs := ag.AllMessages(); len(msgs) > 0 {
    m.blocks = blocksFromMessages(msgs)
    m.tasks = tasksFromMessages(msgs)
}
```

保留 `blocksFromMessages(msgs)` 的调用，只是 task 来源改为 `TaskManager`。

完整的初始化段：

```go
if msgs := ag.AllMessages(); len(msgs) > 0 {
    m.blocks = blocksFromMessages(msgs)
}
if ag.TaskManager() != nil {
    m.tasks = ag.TaskManager().List()
}
```

### Step 7.3: 编译检查

```bash
cd D:\code_practise\project\lab_pj\Lcoder
go build ./pkg/tui
```

Expected: build succeeds.

### Step 7.4: 提交

```bash
cd D:\code_practise\project\lab_pj\Lcoder
git add pkg/tui/events.go pkg/tui/model.go
git commit -m "feat(tui): sync task sidebar from TaskListUpdatedEvent"
```

---

## Task 8: 更新测试

**Files:**
- Modify: `pkg/tui/tasksidebar_test.go`
- Modify: `pkg/agent/loop_test.go`（如果需要）
- Modify: `pkg/agent/checkpoint_test.go`（如果需要）
- Modify: `test/integration/tasks_metrics_test.go`（如果需要）

### Step 8.1: 更新 `pkg/tui/tasksidebar_test.go`

把 `TestHandleEventTodoWriteUpdatesTasksNoBlock` 改为测试 `TaskListUpdatedEvent`：

```go
func TestHandleEventTaskListUpdatedUpdatesTasksNoBlock(t *testing.T) {
    m := &Model{width: 100, input: NewInputModel(), agent: &fakeAgent{}}
    before := len(m.blocks)
    m.handleEvent(events.TaskListUpdatedEvent{
        Base: events.Base{Type: events.TaskListUpdated, Turn: 1},
        Tasks: []task.Task{
            {Text: "a", Status: task.StatusInProgress},
        },
    })
    if len(m.tasks) != 1 || m.tasks[0].Status != task.StatusInProgress {
        t.Fatalf("TaskListUpdatedEvent should populate tasks, got %+v", m.tasks)
    }
    if len(m.blocks) != before {
        t.Fatalf("task update must NOT append a conversation block, blocks grew by %d", len(m.blocks)-before)
    }
}
```

### Step 8.2: 更新 `pkg/tui/session_reload_test.go` 中的 fakeAgent

`fakeAgent` 需要实现 `TaskManager()` 方法。在 `pkg/tui/session_reload_test.go` 或相关 fake 定义中添加：

```go
func (a *fakeAgent) TaskManager() *task.Manager {
    // Return nil or a manager pre-loaded from msgs if needed.
    return nil
}
```

如果 `fakeAgent` 已持有 `msgs`，也可以在这里从 `tasksFromMessages(a.msgs)` 创建并返回一个 `task.Manager`。

### Step 8.3: 添加 `pkg/agent` checkpoint 测试

在 `pkg/agent/checkpoint_test.go` 中新增测试：

```go
func TestCheckpointIncludesTaskManagerState(t *testing.T) {
    // setup agent with task manager
    ag := newTestAgent(t)
    _, _, _ = ag.taskMgr.ReplaceAll([]task.Task{
        {Text: "step one", Status: task.StatusDone},
        {Text: "step two", Status: task.StatusInProgress},
    })

    cp, err := ag.Checkpoint()
    if err != nil {
        t.Fatalf("checkpoint failed: %v", err)
    }
    if cp.Runtime.TaskManagerState == nil {
        t.Fatal("checkpoint missing task manager state")
    }
    if len(cp.Runtime.TaskManagerState.Tasks) != 2 {
        t.Fatalf("expected 2 tasks in checkpoint, got %d", len(cp.Runtime.TaskManagerState.Tasks))
    }
}
```

### Step 8.4: 运行测试

```bash
cd D:\code_practise\project\lab_pj\Lcoder
go test $(go list ./... | grep -v 'reference/Shannon') -count=1
```

Expected: all tests PASS.

### Step 8.5: 提交

```bash
cd D:\code_practise\project\lab_pj\Lcoder
git add pkg/tui/tasksidebar_test.go pkg/tui/session_reload_test.go pkg/agent/checkpoint_test.go
git commit -m "test: update tests for TaskManager integration"
```

---

## Task 9: 集成验证

**Files:**
- None (manual/CLI verification)

### Step 9.1: 构建 CLI

```bash
cd D:\code_practise\project\lab_pj\Lcoder
go build -o lcoder ./cmd/lcoder
```

### Step 9.2: 运行一次多步任务，观察 task 侧栏

```bash
cd D:\code_practise\project\lab_pj\Lcoder
.\lcoder "帮我规划一个小任务：1. 查看当前目录 2. 读取 README 3. 总结内容"
```

Expected: TUI 右侧 task 侧栏显示三个任务，模型会调用 `todo_write` 更新状态。

### Step 9.3: 模拟上下文压缩

可以通过临时调低 `keepRecent` 或调低 context budget 来触发压缩。观察压缩后 task 侧栏是否仍然保留。

### Step 9.4: 模拟崩溃恢复

在运行过程中按 `Ctrl+C`，然后使用 `--continue` 恢复：

```bash
cd D:\code_practise\project\lab_pj\Lcoder
.\lcoder -c
```

Expected: task 侧栏恢复到崩溃前状态。

### Step 9.5: 提交

无需代码变更，记录验证结果。

---

## Task 10: 清理与文档

**Files:**
- Modify: `docs/superpowers/specs/2026-07-04-task-manager-design.md`
- Modify: `docs/superpowers/plans/2026-07-04-task-manager.md`（本文件，标记完成）

### Step 10.1: 更新设计文档状态

把 `docs/superpowers/specs/2026-07-04-task-manager-design.md` 的头部状态改为：

```markdown
状态：已实现
```

### Step 10.2: 运行最终 vet 和测试

```bash
cd D:\code_practise\project\lab_pj\Lcoder
go vet $(go list ./... | grep -v 'reference/Shannon')
go test $(go list ./... | grep -v 'reference/Shannon') -count=1 -race
```

Expected: no vet warnings, all tests PASS.

### Step 10.3: 提交

```bash
cd D:\code_practise\project\lab_pj\Lcoder
git add docs/superpowers/specs/2026-07-04-task-manager-design.md
git commit -m "docs: mark TaskManager design as implemented"
```

---

## Self-Review Checklist

### Spec Coverage

| Spec Requirement | Implementing Task |
| --- | --- |
| 独立 `TaskManager` 组件 | Task 1 |
| `ReplaceAll` + reconciliation | Task 1 |
| `Snapshot`/`Restore` | Task 1, Task 4 |
| Checkpoint 保存 task 状态 | Task 3, Task 4 |
| `todo_write` 通过 executor 更新 TaskManager | Task 5 |
| Ephemeral reminder 注入 | Task 6 |
| TUI 通过独立事件同步 | Task 2, Task 7 |
| 向后兼容降级路径 | Task 4 |

### Placeholder Scan

- 无 TBD/TODO。
- 所有代码步骤包含完整代码。
- 所有命令包含预期结果。

### Type Consistency

- `TaskManager` 方法签名在 Task 1 中定义，后续任务保持一致。
- `RuntimeSnapshot.TaskManagerState` 类型为 `*task.ManagerState`。
- `events.TaskListUpdatedEvent.Tasks` 类型为 `[]task.Task`。

### Potential Issues Flagged

1. `TaskManager.Subscribe` 回调不持有锁，调用方获得快照。
2. `executor` 处理 `todo_write` 时使用运行 context emit 事件，避免 `context.Background()`。
3. 移除 `UnresolvedTodosReminder` 时同步清理 `cmd/lcoder/main.go` 中的 `WithReminderProducer` 调用。
4. `fakeAgent` 和测试 double 需要实现 `TaskManager()` 方法。

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-07-04-task-manager.md`. Two execution options:

**1. Subagent-Driven (recommended)** - I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** - Execute tasks in this session using executing-plans, batch execution with checkpoints for review.

Which approach?
