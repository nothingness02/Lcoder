# TaskManager 独立组件设计

日期：2026-07-04
状态：已实现

## 背景与问题

当前 task 系统已实现为最小版本：LLM 通过 `todo_write` 工具声明任务列表，TUI 从消息历史中解析最后一次调用并渲染侧栏，`UnresolvedTodosReminder` 也从消息历史中扫描未完成任务。

但现有实现存在三个核心问题：

1. **上下文压缩后 task 丢失**：`contextmgr.foldOlder` 会把旧消息折叠成摘要，原始 `todo_write` 工具调用的参数随之丢失。若最近的 `todo_write` 不在 `keepRecent` 保留的 tail 内，TUI 和 agent 都无法再识别任务。
2. **崩溃恢复不可靠**：checkpoint 不保存 task 列表，恢复完全依赖 session。若崩溃前 session 已被 compact 成摘要，任务列表无法重建。
3. **模型可能覆盖/偏向**：`todo_write` 是全量替换语义，但代码没有校验。模型可能遗漏未完成的旧 task，或在旧任务被压缩遗忘后创建新的局部列表，导致任务偏向。

## 目标

将 task 管理提升为与 `contextmgr.Manager` 同级的独立一等组件，使任务列表：

- 不依赖消息历史即可在运行时存在；
- 可被 checkpoint 显式保存和恢复；
- 在 `todo_write` 调用时接受完整性校验，自动补回被遗漏的未完成任务；
- 通过独立事件驱动 TUI 更新，完全解耦于消息历史。

## 核心理念

Task 仍是 **LLM 主动声明的计划**，但 agent 将其视为**受保护的运行时状态**而非聊天记录的副产品。

- `todo_write` 仍写入消息历史，作为模型可观察的声明和审计轨迹。
- `TaskManager` 持有当前 task 列表的权威副本。
- TUI、ReminderProducer、checkpoint 都从 `TaskManager` 读取，不再反向扫描历史。

## 最终方案选型

| 维度 | 选择 |
| --- | --- |
| 组件形态 | 独立 `pkg/task.TaskManager`，与 `contextmgr.Manager` 同级 |
| 更新语义 | `ReplaceAll` + 完整性校验，自动补回遗漏的未完成任务并附带警告 |
| 上下文注入 | Ephemeral reminder，每 turn 由 `TaskManager` 生成 |
| TUI 同步 | 独立 `TaskListUpdatedEvent`，TUI 订阅该事件 |
| 持久化 | checkpoint 保存 `TaskManagerState`；缺失时降级从历史重建 |

## 架构与数据流

```
┌─────────────────────────────────────────────────────────────────────────┐
│                                LLM                                      │
│  todo_write({todos:[...]})  ────────┐                                   │
└─────────────────────────────────────┼───────────────────────────────────┘
                                      ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                          executor (agent 内部)                           │
│  1. 解析 args["todos"]                                                 │
│  2. 调用 taskMgr.ReplaceAll(tasks)                                      │
│     → 与旧列表按 text 做 reconciliation                                │
│     → 自动补回遗漏的 pending/in_progress task                           │
│     → 发出 TaskListUpdatedEvent                                         │
│  3. 返回带警告摘要的 ToolExecutionResult                               │
└─────────────────────────────────────┬───────────────────────────────────┘
                                      │
                    ┌─────────────────┼─────────────────┐
                    ▼                 ▼                 ▼
            ┌───────────┐     ┌─────────────┐    ┌────────────┐
            │  TUI侧栏  │     │ Checkpoint  │    │ Reminder   │
            │ (订阅事件) │     │ (快照/恢复)  │    │ (每 turn)  │
            └───────────┘     └─────────────┘    └────────────┘
```

### 运行时数据流

1. **Turn 开始**：`Agent.run` 调用 `refreshEphemeralReminders`。
   - `TaskManager.FormatReminder()` 生成当前未完成 task 的提醒文本。
   - 注入 contextmgr 的 ephemeral reminders。
2. **模型调用 `todo_write`**：`executor` 调用 `taskMgr.ReplaceAll(parsedTasks)`。
3. **Reconciliation**：`TaskManager` 比较新旧列表，补回遗漏的未完成任务，生成 warnings。
4. **状态变更**：`TaskManager` 更新内部列表，发出 `TaskListUpdatedEvent`。
5. **TUI 响应**：TUI 订阅事件，更新 `m.tasks` 并重渲染侧栏。
6. **Checkpoint 边界**：`Agent.Checkpoint` 调用 `taskMgr.Snapshot()` 写入 `RuntimeSnapshot`。

## 组件清单

### 1. `pkg/task/manager.go`（新增）

```go
package task

type Manager struct {
    mu    sync.RWMutex
    tasks []Task
    subs  []func([]Task)
}

func NewManager() *Manager

// ReplaceAll 用新列表替换旧列表，自动补回遗漏的未完成任务。
// 返回最终采用的列表、warnings（被补回的 task 文本）、以及解析/校验错误。
func (m *Manager) ReplaceAll(tasks []Task) (reconciled []Task, warnings []string, err error)

func (m *Manager) List() []Task
func (m *Manager) Counts() (done, inProgress, pending int)
func (m *Manager) FormatReminder() string

// Subscribe 注册一个任务列表变更回调。回调在锁释放后以当前列表的快照被调用，
// 调用方无需担心并发或死锁。
func (m *Manager) Subscribe(fn func([]Task))

func (m *Manager) Snapshot() ManagerState
func (m *Manager) Restore(state ManagerState) error
```

**Reconciliation 规则**：

- 以 `Task.Text` 作为 key（大小写敏感，不去重）。
- 新列表中存在的 task：按新列表应用其 status。例如模型可将旧 `pending` 推进为 `in_progress` 或 `done`。
- 旧列表中状态为 `pending` 或 `in_progress`、但新列表中没有的 task：自动补回到最终列表，status 保持旧值，并产生 warning。
- 旧列表中状态为 `done`、新列表中没有的 task：允许删除，不补回。
- 空 `text` 或非法 status 的 task 直接返回 error。

### 2. `pkg/task/state.go`（新增）

```go
package task

// ManagerState 是 TaskManager 的可序列化快照。
type ManagerState struct {
    Tasks []Task `json:"tasks"`
}
```

### 3. `pkg/events/task.go`（新增）

将 Task 相关事件定义在系统级事件包中，保持 `pkg/task` 为无外部依赖的纯数据包：

```go
package events

const EventTypeTaskListUpdated EventType = "task_list_updated"

type TaskListUpdatedEvent struct {
    Base
    Tasks []task.Task `json:"tasks"`
}
```

`pkg/task` 不依赖 `pkg/events`；`TaskManager` 通过泛型回调 `func([]task.Task)` 通知订阅者，`Agent` 层负责把回调转发为 `events.TaskListUpdatedEvent`。

### 4. `pkg/task/task.go`（已有，扩展）

保持现有 `Task`、`Status`、`Parse`、`Counts`，不再重复定义 schema。

### 5. `pkg/agent` 改动

#### `Agent` 结构体

```go
type Agent struct {
    // ... 现有字段 ...
    taskMgr *task.Manager
}
```

#### `New` / `NewWithObservability`

创建 `TaskManager` 实例：

```go
ag.taskMgr = task.NewManager()
```

#### `refreshEphemeralReminders`

替换现有 `UnresolvedTodosReminder`，改为：

```go
func (a *Agent) refreshEphemeralReminders() {
    a.mgr.ClearEphemeralReminders()
    if r := a.taskMgr.FormatReminder(); r != "" {
        a.mgr.SetEphemeralReminders([]string{r})
    }
}
```

> 现有 `agent.Config.ReminderProducers` 可继续保留，`TaskManager`  reminder 可作为默认 producer 注入。

#### `CheckpointWithReason`

在构造 `RuntimeSnapshot` 时加入：

```go
Runtime: &checkpoint.RuntimeSnapshot{
    // ... 现有字段 ...
    TaskManagerState: a.taskMgr.Snapshot(),
},
```

#### `Restore`

优先从 checkpoint 恢复：

```go
if cp.Runtime.TaskManagerState != nil {
    _ = a.taskMgr.Restore(*cp.Runtime.TaskManagerState)
} else {
    // 降级：从消息历史重建
    if tasks := tasksFromMessages(a.mgr.AllMessages()); len(tasks) > 0 {
        _, _, _ = a.taskMgr.ReplaceAll(tasks)
    }
}
```

### 6. `pkg/agent/executor.go` 改动

`executor` 需要访问 `TaskManager`。可通过构造时传入：

```go
type executor struct {
    // ... 现有字段 ...
    taskMgr *task.Manager
}
```

在处理 `todo_write` 工具时（可在 `AfterToolCallHook` 或工具执行路径中），调用：

```go
if call.Name == task.ToolName {
    parsed, err := task.Parse(args["todos"])
    if err != nil {
        return // 返回错误结果
    }
    reconciled, warnings, err := e.taskMgr.ReplaceAll(parsed)
    // 将 warnings 附加到 tool result 文本中
}
```

> 采用 `executor` 持有 `TaskManager` 引用的方式。工具保持无状态，`executor` 负责协调并把 `warnings` 注入 `ToolExecutionResult`。这保持了工具定义的纯洁性，也让错误处理和事件emit集中在一处。

### 7. `pkg/tui` 改动

- `Model.tasks` 继续存在，但来源改为 `TaskListUpdatedEvent`。
- `handleEvent` 新增对 `events.TaskListUpdatedEvent` 的处理：
  ```go
  case events.TaskListUpdatedEvent:
      m.tasks = e.Tasks
      m.updateSizes()
  ```
- 移除对 `ToolExecutionStartEvent` 中 `todo_write` 的特殊处理（或保留作为兼容/审计展示）。
- 启动时从 agent 读取当前列表初始化：
  ```go
  m.tasks = setup.ag.TaskManager().List()
  ```
  需要 `Agent` 暴露 `TaskManager()` 方法。

### 8. `pkg/checkpoint` 改动

`RuntimeSnapshot` 新增字段：

```go
type RuntimeSnapshot struct {
    // ... 现有字段 ...
    TaskManagerState *task.ManagerState `json:"task_manager_state,omitempty"`
}
```

## 错误处理

| 场景 | 行为 |
| --- | --- |
| 模型传入非法 status / 空 text | `ReplaceAll` 返回 error，`todo_write` 工具结果标记为 error，LLM 自我纠正 |
| 模型遗漏未完成的旧 task | 自动补回，status 保持旧值，工具结果附带 warning 文本 |
| 模型将已完成 task 删除 | 允许删除，不补回 |
| checkpoint 中无 `TaskManagerState` | 启动时从消息历史扫描最近一次 `todo_write` 降级重建 |
| TUI 收到 `TaskListUpdatedEvent` 但解析失败 | 不应发生（数据已是 `[]Task`），若发生则记录 error 并忽略 |

## 边界情况

### 与上下文压缩的交互

- `TaskManager` 状态不存放在 `contextmgr` 的 block 中，因此 `foldOlder` 不会影响 task 列表。
- `todo_write` 工具调用仍写入消息历史，但即便该消息被压缩，运行时状态和 checkpoint 仍有完整副本。
- 只有在**旧 checkpoint + 已压缩 session** 的极端组合下，才需要依赖历史重建；这是降级路径，不是主路径。

### 与 `WithMode` 的交互

`WithMode` 克隆 agent 时，应共享同一个 `TaskManager` 或克隆其快照。由于 mode 切换通常不改变任务列表，建议共享引用；若需要隔离，可在实现计划中进一步讨论。

### 与崩溃 checkpoint 的交互

`writeCrashCheckpoint` 调用 `Agent.CheckpointWithReason(checkpoint.ReasonCrash)`，会自动包含 `TaskManagerState`。因此崩溃恢复后任务列表可还原。

## 测试计划

### `pkg/task`

- `ReplaceAll`：正常替换、状态推进、遗漏补回、已完成项删除、非法输入报错。
- `Counts` / `FormatReminder`：空列表、全完成、有未完成。
- `Snapshot` / `Restore`：深拷贝验证。

### `pkg/agent`

- `Checkpoint` / `Restore` 包含 task 状态。
- `Restore` 降级路径：从消息历史重建。
- `refreshEphemeralReminders` 使用 `TaskManager` 生成提醒。

### `pkg/tools/builtin`

- `todo_write` 工具返回结果包含 warning 当任务被补回。

### `pkg/tui`

- `handleEvent` 响应 `TaskListUpdatedEvent`。
- 启动时从 agent 初始化 task 列表。

### 集成测试

- 触发上下文压缩后，task 侧栏和 reminder 仍然存在。
- 崩溃 checkpoint 恢复后，task 状态与崩溃前一致。

## 不做（YAGNI）

- 不做任务优先级、assignee、deadline、子任务、依赖关系。
- 不做侧栏内直接交互（勾选/删除）。
- 不做任务持久化的独立存储文件（仍由 checkpoint 和 session 覆盖）。
- 不改动 `todo_write` 的 JSON Schema，保持三态模型。

## 依赖与影响

- 新增 `pkg/task/manager.go`、`pkg/task/state.go`。
- 修改 `pkg/agent/loop.go`、`pkg/agent/checkpoint.go`、`pkg/agent/state.go`（若选择共享引用则改动小）。
- 修改 `pkg/agent/executor.go` 或 `pkg/tools/builtin/todo.go` 以接入 `TaskManager`。
- 修改 `pkg/tui/events.go`、`pkg/tui/model.go`。
- 修改 `pkg/checkpoint/checkpoint.go`（`RuntimeSnapshot` 结构）。
- 移除或弃用 `pkg/agent/reminders.go` 中的 `UnresolvedTodosReminder`，功能由 `TaskManager.FormatReminder` 替代。

## 后续演进

- 当需要更复杂的任务语义（blocker、优先级、子任务）时，只需扩展 `pkg/task` 内部模型和 `ReplaceAll` 的 reconciliation 规则，Agent 核心循环无需改动。
- 可考虑让 `TaskManager` 支持观察者模式之外的查询接口，供 headless/CLI 模式使用。
