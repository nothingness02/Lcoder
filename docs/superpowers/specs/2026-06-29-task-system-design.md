# Task 系统设计

日期:2026-06-29
状态:已批准设计,待出实现计划

## 目标

为 Lcoder agent 增加一套 task(任务分段)系统:LLM 在处理多步任务时主动声明一份任务清单,TUI 把清单渲染到右侧栏,让用户实时看到 agent 的计划与进度。

## 核心理念

Task 是 **LLM 主动声明的计划**,不是系统按 turn 机械分段。只有 LLM 知道任务的语义边界(何时从"探索"转入"实现")。因此:

- **声明**是工具层职责:LLM 通过内置工具 `todo_write` 全量声明任务列表。
- **展示**是 TUI 层职责:TUI 监听该工具的执行事件,把列表渲染到右侧栏。

两层解耦。非 TUI(headless/CLI)运行时,任务声明仍以 tool call 形式留在 transcript 中,只是不渲染侧栏。

## 选型(已确认)

| 维度 | 选择 |
| --- | --- |
| 任务来源 | 作为工具,LLM 驱动 |
| 工具形态 | 单工具 `todo_write`,每次传全量、整体替换 |
| 状态模型 | 三态:`pending` / `in_progress` / `done` |
| 持久化 | 内存态,resume 时从历史里最后一次 `todo_write` 重建 |
| 侧栏显示 | 有任务自动显示、无任务自动隐藏,`Ctrl+T` 手动切换 |

## 架构与数据流

```
LLM ──todo_write({todos:[...]})──> Executable.Execute
                                        │ 校验 + 返回文本摘要 ("4 tasks: 1 done...")
                                        ▼
                              agent loop 照常发 ToolExecutionStart{ToolName, Args}
                                        │ (Args 已是 map[string]any,含完整 todos)
                                        ▼
                              TUI handleEvent: ToolName=="todo_write"
                                        │ → task.Parse(Args) → m.tasks 全量覆盖
                                        ▼
                              updateSizes + 右侧栏重渲染
```

**关键决策:不新增事件类型、不给工具塞 bus 引用。** TUI 当前已从 `ToolExecutionStart` 派生全部 UI 状态,task 复用同一通道——`ToolExecutionStartEvent.Args` 里就带着完整 todos。工具本身无状态:只校验 schema + 返回一行确认文本。这是最小侵入、与现有事件驱动架构一致的做法。

## 侧栏视觉(终端 ASCII,即用户所见)

```
┌─ 对话主区 ────────────────────────┐┌─ Tasks ──────────┐
│ > 帮我重构 auth 模块               ││ ✓ 阅读 auth 现状  │
│                                    ││ ▸ 拆分 handler    │
│ [assistant] 我先看一下结构...      ││ ○ 写单测          │
│ ⏵ 3 tools used                     ││ ○ 跑测试验证      │
│                                    ││                  │
│ [composer]                         ││ 2/4 完成          │
└────────────────────────────────────┘└──────────────────┘
```

状态字形:`○ pending` / `▸ in_progress` / `✓ done`。

## 组件清单

### 1. `pkg/task/`(新,schema 单一真相源)

```go
type Status string // "pending" | "in_progress" | "done"
type Task struct { Text string; Status Status }

func Parse(raw any) ([]Task, error)             // 校验 todos 数组,非法 status / 空 text 报错
func Counts(tasks []Task) (done, inProgress, pending int)
```

工具和 TUI 都依赖它,避免 schema 重复定义。

### 2. `pkg/tools/builtin/todo.go`(新工具)

- `Definition()`:
  - `name = "todo_write"`
  - description 写清用法:多步任务先规划;开工前把对应项标 `in_progress`;完成后标 `done`;每次调用都传全量列表。
  - parameters JSON Schema:`{ todos: [ { text: string, status: enum["pending","in_progress","done"] } ] }`
  - `ExecutionMode: Sequential`(保证 UI 更新相对其他工具有序)。
- `Execute()`:`task.Parse(args["todos"])` → 失败返回 error(让 LLM 自我纠正)→ 成功返回 `ToolResult` 文本摘要(如 `Updated 4 tasks: 1 done, 1 in progress, 2 pending`)。
- 在 `pkg/tools/builtin/init.go` 注册:`tools.DefaultFactories.Register("todo_write", NewTodoWrite)`。

### 3. TUI 改动

- `Model` 新增字段:`tasks []task.Task`、`taskSidebarHidden bool`。
  - 可见性 = `len(tasks) > 0 && !taskSidebarHidden && width >= 60`。
- `pkg/tui/events.go` `handleEvent`:新增对 `ToolExecutionStartEvent` 的处理,当 `ToolName == "todo_write"` 时 `task.Parse(e.Args["todos"])` 全量覆盖 `m.tasks`;解析失败保留旧值。随后触发 `updateSizes` / 重渲染。
- `pkg/tui/tasksidebar.go`(新):`renderTaskSidebar(tasks []task.Task, height int) string`——圆角边框、固定宽 28、每行 `字形 + 截断文本`、底部 `N/M 完成`,复用 `styleDim` / `styleAccent` / `styleSuccess`。
- `pkg/tui/view.go` `View()`:可见时 `lipgloss.JoinHorizontal(lipgloss.Top, mainArea, sidebar)`,其中 `mainArea = JoinVertical(viewport, bottomRegion)`。`stateStartup` / `stateSessionPicker` / `stateExtensions` / `stateProvider` 等全屏态不受影响。
- `pkg/tui/model.go` `updateSizes()`:可见时 `mainWidth = width - 28`(下限保护),viewport 与 composer 宽度改用 `mainWidth`。
- `pkg/tui/keys.go`:`handleInputKey` 与 `handleProcessingKey` 加 `Ctrl+T`,切换 `taskSidebarHidden` 并重算尺寸。
- `pkg/tui/menu.go`:新增 `/tasks` 命令(切换侧栏显隐,与 `/tools` 同类)。

### 4. Resume(内存态重建)

- `tasksFromMessages(msgs []models.AgentMessage) []task.Task`:倒序扫描消息,找最后一次 `todo_write` tool call,解析其 arguments 重建任务列表。
- 在 session 载入 Model 的位置调用一次。

## 错误处理

- 工具校验失败 → 返回 error,LLM 看到后重发;全量替换天然自愈,下一次正确调用直接覆盖。
- TUI 解析失败 → 保留旧 `m.tasks`,不崩溃。
- 终端宽度 < 60 → 自动隐藏侧栏,避免主区被挤坏。

## 测试

- `pkg/task`:`Parse` 合法 / 非法 status / 空 text;`Counts` 计数。
- 工具:`Definition` 形状;`Execute` 返回摘要文本;非法输入报错。
- TUI:`renderTaskSidebar` 输出含字形与 `N/M` 计数;`handleEvent` 从 `todo_write` start 事件更新 `m.tasks`;`updateSizes` 在可见时收窄 viewport 宽度;`tasksFromMessages` 能从历史重建。

## 不做(YAGNI)

- 不做多状态(blocked / cancelled)。
- 不做细粒度 CRUD 工具。
- 不做 session 持久化层(仅内存态 + 历史重建)。
- 不做侧栏内交互(增删/勾选任务)。
- 不新增事件类型、不给工具塞 bus。
