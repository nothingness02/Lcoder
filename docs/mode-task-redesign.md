# Mode 与 Task 系统设计对比与重构建议

## 一、各 Agent 设计

### Kimi Code — Goal（目标）系统

Kimi Code **没有 todo 列表**，用的是 **Goal（单一目标）** 系统：

```
GoalStatus: active → paused/blocked → complete(transient)

- 每个 agent 同时只有 1 个 goal
- goal 有 budget（turns/tokens/墙钟），达到上限自动 blocked
- goal 有 completion_criterion（完成标准）
- 生命周期由 GoalMode 驱动
- 状态变化时才注入 reminder，不是每 turn
```

关键设计：
- **单一目标**而非多任务列表——避免"有 N 个未完成任务"的焦虑
- **完成标准**（criterion）——模型知道自己何时算完成，不会过度确认
- **预算上限**——goal 有 token/turn 预算，达到自动停止（防死循环）
- **状态机**——active/paused/blocked/complete，只有 active 才驱动续跑

### Kimi Code — Plan 模式

Plan 模式是**独立于 goal 的工作流**，有专门的 plan 文件：

```
PlanMode:
- 进入时注入完整 reminder（1 次）
- 之后 2 turn 内不注入（PLAN_MODE_DEDUP_MIN_TURNS=2）
- 之后每 5 turn 全量刷新，中间用 sparse
- 有 plan 文件路径（用户可追踪）
- 退出时注入 exit reminder（1 次）
```

关键设计：
- **去重窗口**：注入后 2 turn 静默
- **plan 文件**：方案落盘，模型不必在上下文里记住
- **明确的结束动作**：ExitPlanMode 工具，模型知道何时结束

### Pi — 无独立 mode/task

Pi 没有 mode 系统（只有 run mode：interactive/print/rpc），没有 todo 列表。全靠系统 prompt + steering。

### Claude Code — TodoList + Plan

Claude Code 的 todo 在系统 prompt 中静态展示，plan 模式有专门的 plan 文件。**不每 turn 注入提醒**——todo 变化时更新，plan 一次性注入。

## 二、Lcoder 当前设计的问题

### Mode 问题

| 问题 | 现状 | 影响 |
|------|------|------|
| 每 turn 注入 | `applyMode()` 每轮调 `modeReminder()` | 模型每轮读到模式约束 → 谨慎 |
| 无去重窗口 | 对比 Kimi Code 的 2-turn 静默 | 噪音 |
| 无 plan 文件 | 方案只存在于对话中 | 长任务方案丢失 |
| 无明确结束工具 | `switch_mode` 但无 ExitPlanMode 语义 | 模型不知道何时"计划完成" |
| 无 plan 审查 | `require_approval_to_exit` 只是开关 | 无结构化的方案审查 |

### Task 问题

| 问题 | 现状 | 影响 |
|------|------|------|
| 每 turn 提醒 | `FormatReminder()` 每轮注入 | "不要停"命令式 |
| 无完成标准 | todo 只有标题，无"完成 = 什么" | 模型无法判断完成 |
| 无预算 | 无 turn/token 上限 | 死循环风险 |
| 多任务并列 | 所有任务同级 | 模型不知道优先级 |

## 三、重构建议（大改）

### 方案 A：引入 Goal 系统（对标 Kimi Code）

```
新增:
  goal.go          — Goal 结构 + 状态机 (active/paused/blocked/complete)
  goal_injector.go — 状态变化时注入 reminder
  
配置:
  goals:
    objective: "重构 handler.go"
    completion_criterion: "所有测试通过，接口文档更新"
    budget: { max_turns: 10, max_tokens: 20000 }
```

**替换**：todo_write 保留但降级为"进度追踪"，goal 成为主任务载体。

**注入策略**：
```
goal 创建   → 注入 objective + criterion + budget
goal 更新   → 注入变更
goal 完成   → 注入完成通知
goal 阻塞   → 注入阻塞原因
其他 turn   → 不注入
```

### 方案 B：保留 Mode 但修复注入频率（小改）

```go
// mode.go 增加
const modeReminderDedupMinTurns = 2

func (a *Agent) modeReminder() string {
    // 注入后 2 turn 内不注入
    if assistantTurnsSince < modeReminderDedupMinTurns {
        return ""
    }
    // ...
}
```

**对标 Kimi Code 的 `PLAN_MODE_DEDUP_MIN_TURNS`**。

### 方案 C：Task 增加 dirty 标记 + 完成标准

```go
// task.Manager
type Manager struct {
    dirty bool  // 状态变化时置 true
}

// 增加完成标准字段
type Task struct {
    ID          string
    Content     string
    Status      string
    // ★ 新增
    DoneWhen    string  // "完成 = 什么"
    Priority    int     // 优先级
}
```

**注入策略**：仅 dirty 时注入，且措辞中性化。

## 四、推荐组合

```
┌─ Mode 修复（方案 B）────────────────────────────┐
│  modeReminderDedupMinTurns = 2                 │
│  注入后 2 turn 静默                            │
│  每 5 turn 全量刷新（已有）                     │
└────────────────────────────────────────────────┘
       ↓
┌─ Task 修复（方案 C）────────────────────────────┐
│  dirty 标记：仅状态变化时注入                    │
│  DoneWhen 字段：模型知道完成标准                 │
│  中性措辞：去掉 "do not stop"                  │
└────────────────────────────────────────────────┘
       ↓
┌─ 远期：Goal 系统（方案 A）──────────────────────┐
│  单一目标 + 预算 + 完成标准                      │
│  对标 Kimi Code 的 GoalMode                    │
└────────────────────────────────────────────────┘
```

## 五、改动量预估

| 方案 | 文件 | 行数 | 风险 |
|------|------|:---:|:---:|
| B: mode 去重 | `loop.go` + `mode.go` | ~20 | 低 |
| C: task dirty + criterion | `task/manager.go` + `task/task.go` | ~40 | 中 |
| A: goal 系统 | 新增 `goal/` 包 + wiring | ~300 | 高 |

**建议先做 B + C（立即见效），A 作为远期路线图。**
