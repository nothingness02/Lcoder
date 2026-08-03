# Todo 系统设计对比（修正版）

> 修正：之前的 `docs/mode-task-redesign.md` 声称 Kimi Code 没有 todo，**这是错误的**。
> Kimi Code 和 OpenCode 都有完整的 todo 系统。本文给出准确对比。

## 一、Kimi Code 的 Todo

### 工具形态：TodoList 单一工具（读写一体）

```typescript
// todo-list.ts
TodoListInputSchema = z.object({
  todos: z.array({ title: string, status: 'pending'|'in_progress'|'done' }).optional()
})

// 用法：
// TodoList({ todos: [...] })  → 替换全列表
// TodoList({ todos: [] })     → 清空
// TodoList({})                → 查询（不修改）
```

**存储**：`tool_store` 中的 `todo` key（agent 级存储）。

### 写入时提醒

```typescript
// todo-list.ts
const TODO_LIST_WRITE_REMINDER =
  'Ensure that you continue to use the todo list to track progress. ' +
  'Mark tasks done immediately after finishing them, and keep exactly ' +
  'one task in_progress when work is underway.';
```

**提醒时机**：**仅在模型调用 TodoList 写入时**附加在 tool 输出里。不是每 turn 注入！

### 低频 reminder 注入

```typescript
// injection/todo-list.ts
const TODO_LIST_REMINDER_TURNS_SINCE_WRITE = 10;     // 距上次写入 ≥10 turn
const TODO_LIST_REMINDER_TURNS_BETWEEN_REMINDERS = 10; // 距上次提醒 ≥10 turn

getInjection(): string | undefined {
    if (!this.isTodoListActive()) return undefined;
    if (turnsSinceLastWrite < 10 || turnsSinceLastReminder < 10) {
        return undefined;  // ★ 10 turn 内不提醒
    }
    return renderTodoListReminder(...);
}
```

**提醒时机**：todo 工具激活 + 距上次写入 ≥10 turn + 距上次提醒 ≥10 turn。

**措辞**（注意不是命令式）：
```
"The TodoList tool has not been updated recently. If you are working on tasks
that benefit from progress tracking, consider using TodoList to update task
status... This is a gentle reminder; ignore it if not applicable.
Make sure that you NEVER mention this reminder to the user."
```

### 压缩时合并

```typescript
// compaction/full.ts
const todos = storeData[TODO_STORE_KEY] ?? [];
const todoMarkdown = renderTodoList(todos, '## TODO List');
// 压缩摘要中包含 todo 列表
```

**关键设计**：
- todo 存在 tool_store，**不在系统 prompt 里**
- 写入提醒附加在工具输出，**不是系统注入**
- 低频 reminder 有 **10-turn 去重窗口**
- 措辞是"gentle reminder"（温和提醒），**不是命令式**
- 压缩时把 todo 并入摘要

## 二、OpenCode 的 Todo

### 工具形态：`todowrite` 工具

```typescript
// packages/core/src/tool/todowrite.ts
// 有独立的 todo 工具，写入任务列表
```

### 权限控制

```typescript
// agent.ts
todowrite: "deny",  // subagent 默认禁止

// subagent-permissions.ts
// 子 agent 默认 deny todowrite，除非显式允许
```

**关键设计**：
- todo 是**独立工具**，有独立权限
- 子 agent 默认不能写 todo（防止混乱）
- todo dock UI（`session-todo-dock.tsx`）展示在侧边栏

## 三、Claude Code 的 Todo

Claude Code 的 todo 在**系统 prompt** 中：

```
# TodoList

---
- [ ] 任务1
- [ ] 任务2
- [x] 任务3
---

`TodoList` 任务通过 todowrite 工具更新。只有 pending/in_progress/done 三种状态。
```

**关键设计**：
- todo 直接内嵌在系统 prompt（每次请求都发送）
- 但**不额外注入提醒**——todo 列表本身就是状态
- 模型通过 `todowrite` 工具更新，工具返回新列表

## 四、对比总结

| 维度 | Kimi Code | Claude Code | OpenCode | Lcoder |
|------|-----------|-------------|----------|--------|
| 存储 | tool_store（不在 prompt） | 系统 prompt 内嵌 | 独立状态 | Manager 内存 |
| 工具 | TodoList（读写一体） | todowrite | todowrite | todo_write |
| 写入提醒 | 工具输出附加 | 无 | 无 | **每 turn 注入** |
| 低频提醒 | **10-turn 去重** | 无 | 无 | 无 |
| 措辞 | "gentle reminder" | 中性 | 中性 | **"do not stop"** |
| 压缩合并 | ✅ 并入摘要 | 无 | 无 | 需查 |
| 权限 | 普通工具 | 普通工具 | subagent deny | 普通工具 |

## 五、Lcoder 的差距

| 问题 | Lcoder 现状 | 应改为 |
|------|------------|--------|
| 每 turn 注入 | `FormatReminder()` 每轮调用 | 仅状态变化时（dirty 标记） |
| 命令式措辞 | "do not stop until complete" | "gentle reminder, ignore if not applicable" |
| 无去重窗口 | 无 | 10-turn 去重（对标 Kimi Code） |
| 存储位置 | Manager 内存（Agent 专属） | tool_store 或跨 turn 持久 |
| 无压缩合并 | 需查 | 压缩摘要包含 todo |

## 六、修改建议

### 立即修复（小改）

```go
// task/manager.go — 增加 dirty 标记 + 中性措辞
type Manager struct {
    dirty bool  // 状态变化时置 true
}

func (m *Manager) FormatReminder() string {
    if !m.dirty {
        return ""  // ★ 无变化不提醒
    }
    m.dirty = false
    // 中性措辞
    return fmt.Sprintf("You have %d open todo item(s). " +
        "Update the list as you make progress. " +
        "This is a gentle reminder; ignore if not applicable.", remaining)
}
```

### 对标 Kimi Code（中改）

```go
// 增加去重窗口
const todoReminderTurnsSinceWrite = 10
const todoReminderTurnsBetweenReminders = 10

// reminder_coordinator.go
func (rc *reminderCoordinator) Reminders(msgs []models.AgentMessage) []string {
    if rc.taskMgr.IsDirty() {
        // 刚写入 → 附加提醒
        return []string{rc.taskMgr.FormatReminder()}
    }
    // 低频提醒：距上次写入/提醒 ≥10 turn
    if rc.taskMgr.TurnsSinceWrite(msgs) >= 10 &&
       rc.taskMgr.TurnsSinceReminder(msgs) >= 10 {
        return []string{rc.taskMgr.GentleReminder()}
    }
    return nil
}
```

### 远期（大改）

- todo 移到跨 turn 持久存储（对标 tool_store）
- 压缩摘要包含 todo 列表（对标 Kimi Code `renderTodoList`）
- subagent 默认 deny todo（对标 OpenCode）
