# Kimi Code 注射器（Injector）机制

## 一、整体架构

```
InjectionManager
  ├── 每 step 前: inject()        → 遍历 [PluginSessionStart, TodoList, PlanMode, PermissionMode]
  ├── turn 边界:  injectGoal()    → GoalInjector（仅 main agent）
  ├── turn 边界:  injectToolsDiff() → ToolsDiffInjector
  ├── 压缩后:     injectAfterCompaction() → goal + toolsDiff + background tasks + inject()
  └── 上下文变化: onContextClear / onContextCompacted / onContextMessageRemoved
```

**两种调用节奏**：
- **每 step**（beforeStep hook）: 4 个"轻量" injector，各自决定是否静默
- **turn 边界**（turn 开始 + 压缩后）: goal 和 tools-diff，避免每 step 注入破坏 cache

## 二、DynamicInjector 基类

```typescript
abstract class DynamicInjector {
    protected injectedAt: number | null = null;  // 上次注入时 history 长度

    async inject(): Promise<void> {
        const injection = await this.getInjection();
        if (injection) {
            this.injectedAt = this.agent.context.history.length;
            this.agent.context.appendSystemReminder(injection, {
                kind: 'injection',
                variant: this.injectionVariant,  // 每个子类有唯一 variant
            });
        }
        // getInjection() 返回 undefined → 完全静默
    }

    // 生命周期回调
    onContextClear()       { this.injectedAt = null; }  // 会话重置
    onContextCompacted()   { this.injectedAt = null; }  // 压缩后强制重注
    onContextMessageRemoved(index) { /* 调整 injectedAt 索引 */ }
}
```

**核心思想**：`inject()` 每 step 都跑，但 `getInjection()` 返回 `undefined` 就不产生任何上下文增长。**静默是默认，注入是例外。**

## 三、Reminder 的存储形态

```typescript
// context/index.ts
appendSystemReminder(content, origin) {
    const text = `<system-reminder>\n${content.trim()}\n</system-reminder>`;
    this.appendMessage({
        role: 'user',            // ← 以 user 消息形态追加
        content: [{ type: 'text', text }],
        toolCalls: [],
        origin,                  // ← 标记来源，用于去重/回放路由
    });
}
```

**关键设计**：
- reminder 是 `<system-reminder>` 包裹的 user 消息
- `origin = { kind: 'injection', variant: 'xxx' }` 是**去重和回放路由的钥匙**
- 注入型消息在 **replay/transcript 中跳过**（用户看不到）

## 四、6 个 Injector 详解

### 1. TodoListReminderInjector（每 step，10-turn 去重）

```typescript
// variant: 'todo_list_reminder'
getInjection():
  - todo 工具未激活 → undefined
  - 反向遍历 history 数 turn:
    - turnsSinceLastWrite < 10 → undefined
    - turnsSinceLastReminder < 10 → undefined
  - 两者都 ≥10 → 温和提醒 + 当前 todo 列表
```

### 2. PlanModeInjector（每 step，状态机变体）

```typescript
// variant: 'plan_mode'
getInjection():
  - 未激活:
    - 之前激活过 → exitReminder（"已退出 plan 模式"）
    - 否则 → undefined
  - 激活:
    - 首次 → reentryReminder（有旧 plan 文件）或 fullReminder
    - 距上次注入 ≥5 assistant turn → fullReminder
    - ≥2 turn → sparseReminder
    - <2 turn → undefined（★ 去重窗口）
```

### 3. PermissionModeInjector（每 step，变化检测）

```typescript
// variant: 'permission_mode'
private lastMode: PermissionMode;

getInjection():
  - 模式没变且未压缩 → undefined
  - 进入 auto → AUTO_MODE_ENTER_REMINDER
  - 退出 auto → AUTO_MODE_EXIT_REMINDER
  - 其他变化 → undefined
```

**只在模式切换时注入，平时完全静默。**

### 4. PluginSessionStartInjector（每 step，一次性）

```typescript
// variant: 'plugin_session_start'
getInjection():
  - 已经注入过（injectedAt != null）→ undefined
  - history 中已有同 variant → 记录位置，返回 undefined（防重放）
  - 否则 → 渲染插件技能提示
```

**只注入一次**，之后永远静默。

### 5. GoalInjector（turn 边界，每 turn 一次）

```typescript
// variant: 'goal'
// 由 injectGoal() 调用，不是每 step 的 inject() 循环
getInjection():
  - 无 goal → undefined
  - active → buildGoalReminder（目标 + 完成标准 + 预算 + 审计规则）
  - blocked → buildBlockedNote（轻量，不施压）
  - paused → buildPausedNote（轻量，禁止自主推进）
```

**三种状态三种强度**：active 完整提醒 + 预算，blocked/paused 只保持可见性。

### 6. ToolsDiffInjector（turn 边界，diff 检测）

```typescript
// origin: 'system_trigger'（不是 'injection'！）
inject():
  - 计算 loadable 工具集
  - 从 history 折叠已声明集合
  - 算出 added/removed
  - 无差异 → 不注入
  - 有差异 → <tools_added>/<tools_removed> 公告
```

**用 history 做账本**（无内存状态），undo/compaction/resume 自动自愈。

## 五、不同 injector 的节奏对比

| Injector | 调用节奏 | 去重机制 | 静默条件 |
|----------|---------|---------|---------|
| TodoList | 每 step | 10-turn | 距写入/提醒 <10 |
| PlanMode | 每 step | 2-turn / 5-turn | 距注入 <2 |
| PermissionMode | 每 step | 状态比较 | 模式没变 |
| PluginSessionStart | 每 step | injectedAt | 已注入过 |
| Goal | turn 边界 | 每 turn 一次 | 无 goal |
| ToolsDiff | turn 边界 | history diff | 工具集没变 |

## 六、设计要点总结

1. **静默是默认**：`getInjection()` 返回 `undefined` 不产生任何上下文增长，不破坏 prompt cache
2. **origin.variant 是核心**：去重、回放路由、压缩重注都靠它
3. **两种节奏**：轻量 injector 每 step（靠自去重），重量 injector turn 边界（靠调用时机）
4. **状态从 history 推断**：TodoList/PlanMode 反向遍历，ToolsDiff 折叠账本，无内存状态泄漏
5. **压缩后强制重注**：`onContextCompacted()` 重置 `injectedAt`，确保压缩后约束不丢
6. **三种强度**：goal 有 active/blocked/paused 三档提醒强度

## 七、Lcoder 的对应关系

| Kimi Code | Lcoder | 差距 |
|-----------|--------|------|
| DynamicInjector 基类 | `applyMode()` + `refreshEphemeralReminders()` | 无统一注入器抽象 |
| origin.variant | 无 | 无去重/回放路由 |
| 每 step 静默 + 自去重 | 每 turn 硬注入 | **无去重窗口** |
| GoalInjector 三档强度 | 无 | 无 goal 系统 |
| ToolsDiffInjector | `tool_activate` | 无 manifest 公告 |
| 压缩后重注 | 需查 | — |
