# Mode 与 Task 注入机制分析

## 当前注入流程

```
run() 每轮迭代：
  ① turn++ 检查 MaxTurns
  ② DrainSteeringQueue()
  ③ TurnStart event
  ④ refreshEphemeralReminders()   ← 每次 turn
     └── taskMgr.FormatReminder()  → "You have N unfinished todo..."
     └── ReminderProducers()       → 各 producer 输出
  ⑤ maybeCompact()
  ⑥ applyMode()                   ← 每次 turn
     └── modeReminder()
         ├── 完整 prompt (进入/切换/每5turn)
         ├── 稀疏 prompt (中间)
         └── 切换通知 (仅切换时)
  ⑦ streamer.stream()             ← LLM 请求
```

## 注入频率与内容

| 注入项 | 频率 | 内容 | 问题 |
|--------|:---:|------|:---:|
| **任务提醒** | **每个 turn** | "You have N unfinished todo item(s) (M done). Continue working toward them; do not stop until they are complete..." | ❌ 命令式"不要停"，强化模型不确认就继续 |
| **模式完整 prompt** | 进入 + 每 5 turn | 完整 system_prompt | ⚠️ 频率合理但内容长 |
| **模式稀疏 prompt** | 每 turn（非完整时） | SparsePrompt | ⚠️ 仍每 turn 注入 |
| **模式切换通知** | 仅切换时 | "You have switched from X to Y" | ✅ 合理 |

**核心问题：每个 turn 都注入至少一条 reminder。** 模型每轮都读到"模式约束 + 任务未完成提醒"。

## 与 Kimi Code 的对比

### Kimi Code 的 PlanModeInjector

```typescript
const PLAN_MODE_DEDUP_MIN_TURNS = 2;   // ★ 去重下限
const PLAN_MODE_FULL_REFRESH_TURNS = 5;

getVariant(): PlanModeVariant | null {
    if (this.injectedAt === null) return 'full';  // 首次 → 完整
    // 计算自上次注入后的 assistant turn 数
    if (assistantTurnsSince >= 5) return 'full';  // ≥5 → 完整刷新
    if (assistantTurnsSince >= 2) return 'sparse'; // ≥2 → 稀疏
    return null;  // ★ <2 turn → 不注入！
}
```

**关键差异：Kimi Code 在注入后 2 个 turn 内不注入任何提醒。**

```
Kimi Code 时序:
  turn 0: full  (进入模式)
  turn 1: (无注入)   ← 刚提醒过，不需要
  turn 2: sparse
  turn 3: (无注入)
  turn 4: (无注入)
  turn 5: full
  ...

Lcoder 时序:
  turn 0: full  (进入模式)
  turn 1: sparse + 任务提醒
  turn 2: sparse + 任务提醒
  turn 3: sparse + 任务提醒
  turn 4: sparse + 任务提醒
  turn 5: full + 任务提醒
  ...
```

### Kimi Code 无任务列表注入

Kimi Code **没有 todo 列表的每 turn 提醒**。任务状态变化才更新上下文，不会在每轮 LLM 请求中重复"你有 N 个未完成任务"。

## 问题根源：过度确认的诱因

模型每个 turn 收到：
1. "模式约束：你是 plan 模式，只读..." → 强化"我在受限模式"
2. "你有 3 个未完成任务，不要停" → 强化"任务没做完"

叠加效果：
- **过度确认**：模型反复问"我是否应该继续？"因为"不要停"让它觉得停止是错误的
- **重复谨慎**：模型反复检查任务列表、反复确认已完成项
- **token 浪费**：每条 reminder ~30-80 tokens，100 turn 会话浪费 3K-8K tokens

## 建议修复

### 1. 任务提醒降频（仅状态变化时注入）

```go
// task.Manager 增加 dirty 标记
type Manager struct {
    // ...
    dirty bool  // 任务增删改时置 true
}

// FormatReminder 只在 dirty 时输出
func (m *Manager) FormatReminder() string {
    if !m.dirty {
        return ""
    }
    // ... 现有逻辑
}

// refreshEphemeralReminders 只注入变更
func (a *Agent) refreshEphemeralReminders() {
    if !a.taskMgr.IsDirty() {
        return
    }
    a.taskMgr.ClearDirty()
    // ... 注入
}
```

**效果**：任务创建/完成时提醒一次，之后不再重复。模型不会每轮看到"未完成"。

### 2. 模式提醒增加去重窗口（对标 Kimi Code）

```go
const modeReminderDedupMinTurns = 2  // 注入后 2 turn 内不注入

func (a *Agent) modeReminder() string {
    // ...
    if assistantTurnsSince < modeReminderDedupMinTurns {
        return ""  // ★ 刚提醒过，跳过
    }
    // ... 现有逻辑
}
```

**效果**：模式约束在注入后 2 turn 内不重复，减少噪音。

### 3. 移除命令式措辞

```
旧: "You have N unfinished todo item(s). Continue working toward them; do not stop until they are complete or you report a blocker."
新: "You have N open todo item(s). Update them as you make progress."
```

**效果**：中性描述，不强迫模型"不能停"。

### 4. 合并同 turn 的多条 reminder

```go
// BuildTurnRequest 中，ephemeral reminders 合并为一条
```

**效果**：一条 reminder 包含模式和任务信息，而非两条独立消息。

## 影响评估

| 维度 | 影响 |
|------|------|
| token 消耗 | 100 turn 会话省 3K-8K tokens |
| 模型行为 | 减少过度确认、重复检查 |
| 模式约束有效性 | 不变（2 turn 去重不损害约束记忆） |
| 任务追踪 | 状态变化时仍提醒 |

## 参考实现

- **Kimi Code**：`agent/injection/plan-mode.ts` — `PLAN_MODE_DEDUP_MIN_TURNS` + `getVariant()` 返回 null
- **Lcoder 当前**：`loop.go:modeReminder()` — 每 turn 注入
