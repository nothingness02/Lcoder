# Kimi Code 的 10-turn 温和提醒实现机制

## 触发链路

```
每次模型 step 前:
  beforeStep hook
    └── injectionManager.inject()
          └── 遍历所有 DynamicInjector
                ├── TodoListReminderInjector.getInjection()
                ├── PlanModeInjector.getInjection()
                ├── PermissionModeInjector.getInjection()
                └── PluginSessionStartInjector.getInjection()
                      │
                      ├── 返回 undefined → 什么都不注入
                      └── 返回文本 → appendSystemReminder() 追加到上下文尾部
```

**关键**：`inject()` 每 step 都调用，但每个 injector 自己决定**是否返回内容**。返回 `undefined` 就是"静默"——不产生任何上下文增长，不污染 prompt cache。

## TodoListReminderInjector 的核心逻辑

```typescript
// injection/todo-list.ts
const TODO_LIST_REMINDER_TURNS_SINCE_WRITE = 10;      // 距上次写入
const TODO_LIST_REMINDER_TURNS_BETWEEN_REMINDERS = 10; // 距上次提醒

getInjection(): string | undefined {
    // ① todo 工具未激活 → 不提醒
    if (!this.isTodoListActive()) return undefined;

    // ② 从历史反向统计两个 turn 计数
    const counts = getTodoListReminderTurnCounts(this.agent.context.history);

    // ③ 任一计数 < 10 → 不提醒（去重窗口）
    if (counts.turnsSinceLastWrite < 10 ||
        counts.turnsSinceLastReminder < 10) {
        return undefined;
    }

    // ④ 两者都 ≥10 → 返回温和提醒
    return renderTodoListReminder(this.currentTodos());
}
```

## 反向遍历统计（核心）

```typescript
function getTodoListReminderTurnCounts(history): TodoListReminderTurnCounts {
    let foundWrite = false;
    let foundReminder = false;
    let turnsSinceLastWrite = 0;
    let turnsSinceLastReminder = 0;

    // 从历史最后一条消息往回走
    for (let i = history.length - 1; i >= 0; i--) {
        const message = history[i];

        if (message.role === 'assistant') {
            // 遇到 assistant 消息：
            //   - 如果还没找到"最近一次 todo 写入"，turn 计数 +1
            //   - 如果还没找到"最近一次提醒"，reminder 计数 +1
            if (!foundWrite && hasTodoListWrite(message)) foundWrite = true;
            if (!foundWrite) turnsSinceLastWrite += 1;
            if (!foundReminder) turnsSinceLastReminder += 1;
            continue;
        }

        // 遇到注入型提醒消息，标记已找到
        if (!foundReminder && isTodoListReminder(message)) {
            foundReminder = true;
        }

        if (foundWrite && foundReminder) break;
    }

    return { turnsSinceLastWrite, turnsSinceLastReminder };
}
```

### 如何识别"todo 写入"

```typescript
function hasTodoListWrite(message): boolean {
    // 该 assistant 消息是否调用了 TodoList 且传了 todos 参数（写操作）
    return message.toolCalls.some(tc =>
        tc.name === TODO_LIST_TOOL_NAME &&
        JSON.parse(tc.arguments).todos !== undefined  // 有 todos = 写
    );
}
```

### 如何识别"上次提醒"

```typescript
function isTodoListReminder(message): boolean {
    // 该消息是注入型，且 variant 是 todo_list_reminder
    return message.origin?.kind === 'injection' &&
           message.origin.variant === TODO_LIST_REMINDER_VARIANT;
}
```

## DynamicInjector 基类的状态管理

```typescript
abstract class DynamicInjector {
    protected injectedAt: number | null = null;  // 上次注入时 history 长度

    async inject(): Promise<void> {
        const injection = await this.getInjection();
        if (injection) {
            // 记录注入位置 + 追加提醒
            this.injectedAt = this.agent.context.history.length;
            this.agent.context.appendSystemReminder(injection, {...});
        }
        // 返回 undefined → 什么都不做
    }

    // 上下文变化时重置状态
    onContextClear()       { this.injectedAt = null; }
    onContextCompacted()   { this.injectedAt = null; }
    onContextMessageRemoved(index) { /* 调整 injectedAt 索引 */ }
}
```

## 温和提醒的文本

```typescript
function renderTodoListReminder(todos): string {
    let message =
        'The TodoList tool has not been updated recently. ' +
        'If you are working on tasks that benefit from progress tracking, ' +
        'consider using TodoList to update task status. ' +
        'Also consider clearing or rewriting the todo list if it has become stale. ' +
        'Only use it if relevant. ' +
        'This is a gentle reminder; ignore it if not applicable. ' +
        'Make sure that you NEVER mention this reminder to the user.';

    // 附上当前 todo 列表
    if (items.length > 0) {
        message += `\n\nCurrent todo list:\n${items}`;
    }
    return message;
}
```

措辞要点：
- **"gentle reminder; ignore if not applicable"** —— 允许模型忽略
- **"consider using"** —— 建议而非命令
- **"NEVER mention this reminder to the user"** —— 不污染对话

## 时序示例

```
turn 0:  模型调用 TodoList([t1,t2])  → 写入提醒附加在工具输出
turn 1:  inject() → turnsSinceLastWrite=1 <10 → 静默
turn 2:  inject() → =2 <10 → 静默
...
turn 9:  inject() → =9 <10 → 静默
turn 10: inject() → =10 ≥10, turnsSinceLastReminder=10 ≥10 → 注入温和提醒
turn 11: inject() → turnsSinceLastReminder=1 <10 → 静默
...
turn 20: 再次注入温和提醒（若仍无写入）
```

## 关键设计要点

| 设计 | 作用 |
|------|------|
| `undefined` 静默 | 不增长上下文，不破坏 prompt cache |
| 10-turn 去重 | 避免每 step 提醒 |
| 反向遍历 history | 不需要额外状态，从消息历史推断 |
| 写入即重置 | 模型更新过 todo 就不需要提醒 |
| "gentle reminder" | 允许模型忽略，减少"必须继续"压力 |
| 不提及用户 | 提醒是系统消息，不进入对话 |

## Lcoder 的移植建议

```go
// task/manager.go
type Manager struct {
    // ...
    lastWriteTurn   int  // 最近一次 todo 写入的 turn
    lastReminderTurn int // 最近一次提醒的 turn
}

func (m *Manager) ShouldRemind(currentTurn int) bool {
    if m.lastWriteTurn == 0 { return false } // 从未写入不提醒
    if currentTurn-m.lastWriteTurn < 10 { return false }
    if currentTurn-m.lastReminderTurn < 10 { return false }
    return true
}

func (m *Manager) MarkWrite()    { m.lastWriteTurn = turn }
func (m *Manager) MarkReminder() { m.lastReminderTurn = turn }
```

替代反向遍历 history 的方案：在 `todo_write` 工具调用时记录 turn，在注入时比较当前 turn。更简单、无遍历开销。
