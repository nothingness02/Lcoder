# 停止条件对比

## 各 Agent 停止机制

| 停止条件 | Lcoder | Kimi Code | Pi | Kocoro |
|----------|:---:|:---:|:---:|:---:|
| 无 tool calls → 自然完成 | ✅ 默认 | ✅ `end_turn` | ✅ | ✅ |
| 工具 `terminate` 标记 | ✅ | ❌ | ✅ | ❌ |
| `maxSteps` / `max_turns_per_run` 硬上限 | ❌ | ✅ | ❌ | ❌ |
| `stop_reason` 分类 | ❌ | ✅ 6 种 | ❌ | ❌ |
| provider `truncated` 检测 | ❌ | ✅ `max_tokens` | ❌ | ❌ |
| `ShouldStop` hook | ✅ | ❌ (in-loop) | ✅ `shouldStopAfterTurn` | ❌ |
| `ShouldContinueAfterStop` | ✅ | ✅ 核心机制 | ❌ (followUp 替代) | ❌ |
| ~~`FollowUp` 队列~~ | ❌ 已退役(faf1b40) | ❌ | ✅ | ❌ |
| steering 中途注入 | ✅ | ✅ `flushSteerBuffer` | ✅ | ❌ |
| goal 驱动(turn 外) | ❌ | ✅ `driveGoal` | ❌ | ❌ |
| idle 看门狗超时 | ❌ | ❌ | ❌ | ✅ `idleHardTimeout` |
| force_stop (迭代检测) | ❌ | ❌ | ❌ | ✅ |
| abort / 用户中断 | ✅ | ✅ | ✅ | ✅ |
| Phase 状态机 | ❌ | ❌ | ❌ | ✅ |

## 关键差距

### Lcoder 缺失：硬上限

所有 agent 中只有 Lcoder 没有 turn/step 硬上限。如果 LLM 陷入"调工具→失败→重试"的循环，Lcoder 会永远循环直到用户按 Ctrl+C。Kimi Code 有 `maxSteps`，Kocoro 有 `force_stop` 迭代检测。

### Lcoder 缺失：stop_reason 分类

Lcoder 只有二元判断（有 tool_call → 继续，无 → 停止）。Kimi Code 区分 6 种停止原因：

| stop_reason | 含义 | Lcoder 等价 |
|-------------|------|------------|
| `tool_use` | 模型要调工具 | 有 tool calls |
| `end_turn` | 自然完成 | 无 tool calls |
| `max_tokens` | provider 截断 | 无检测 |
| `max_steps` | 达到步数上限 | 无上限 |
| `filtered` | 内容审核 | 无检测 |
| `aborted` | 用户中断 | `EndReasonInterrupted` |

### FollowUp 已退役

`faf1b40` 起 Lcoder 的 followUp 队列被 `ShouldContinueAfterStop` 取代，与 Kimi Code 对齐。Kimi 的同名 hook 可以调 LLM、等异步、检查 goal 预算，比队列强大得多。

### Kocoro 独有：看门狗

生产级 daemon 场景需要的 idle 超时检测——如果 agent 在某个阶段停滞超过 N 秒，自动取消。Lcoder 的 TUI 模式不需要（用户会按 Ctrl+C），但如果未来支持 daemon 模式，这是必须的。

## Kimi Code 的两层结构（源码核对）

Kimi 把"停止"拆成两层，这是理解 `/goal` 实现方式的关键：

### 层 1：turn 内 —— `shouldContinueAfterStop`（`loop/run-turn.ts:190`）

模型给出终止性 stop_reason（非 `tool_use`）时，loop 调 hook 决定是否续一步。hook 收到
`{turnId, stepNumber, usage, stopReason, signal, llm}`——**stopReason、usage、LLM 句柄**
三者俱全。agent 层的实现（`agent/turn/index.ts:884`）是一条**手写优先级链**：

```
0. goal 预算硬顶 → continue:false(确定性天花板,veto 一切续跑)
1. 有 steer 进来的消息 → continue:true(让模型消化)
1.5 print 模式:后台 subagent 未跑完 → 等待 + continue:true
2. goal 刚被标记完成 → 续一步产出总结消息
3. 外部 Stop hook(用户脚本)→ 最多续一次
4. 否则 → continue:false
```

### 层 2：turn 外 —— goal driver(`agent/turn/index.ts:470 driveGoal`)

**`/goal` 不是一个超长 loop,而是 loop 外面包一个 while,反复启动普通 turn:**

```
while true:
    goal 超预算 → markBlocked,结束(不跑模型)
    incrementTurn; runOneTurn(普通 turn,自带 maxSteps 上限)
    turn 结束:
      cancelled → pause,结束
      failed(非步数上限) → pause,结束
      模型已用 UpdateGoal 工具标记完成/放弃 → 结束
      超预算 → markBlocked,结束
      否则 → 注入 GOAL_CONTINUATION_PROMPT,再开一个新 turn
```

要点:
- goal 状态(active/paused/blocked/complete + 预算)由独立的 goal service 持有,
  **模型通过 `update_goal` 工具自己标记完成**——完成判断不依赖 LLM 分类器。
- 预算(token/turn/wall-clock)在 `recordTokenUsage` 时记账,超限经
  `afterStep → stopTurn` 和层 1 的 0 号 veto 双保险掐停。
- turn 之间的"是否继续追求 goal"决策**不在 loop 里**,在 driver 里。loop 保持简单。

### Pi 的对照

Pi 用 followUp 队列:streaming 期间到达的消息排队,agent 要停时若有 followUp 则注入并续跑。
等价于 Kimi 层 1 优先级链的第 1 项,但没有预算、没有 hook、没有 driver 层。

## 完善 Lcoder stop 机制的设计建议

目标:之后可以操控 loop(暂停/续跑/注入),并支持 `/goal` 类指令。**架构结论先行:
照搬 Kimi 两层结构——loop 内只补 StopContext,`/goal` 实现在 loop 外。**

### A. `ShouldContinueAfterStop` 的上下文升级为 `StopContext` ★★★

当前 `TurnSummary` 只有 `{Message, ToolResults, Context}`,hook 无法区分"为什么停"、
无法调 LLM、不知道跑了多少步。对照 Kimi 的 hook 入参补齐:

```go
type StopReason string
const (
    StopEndTurn     StopReason = "end_turn"     // 无 tool calls,自然完成
    StopTerminated  StopReason = "terminated"   // 工具 terminate 标记
    StopMaxTurns    StopReason = "max_turns"    // 硬上限(见 B)
    StopInterrupted StopReason = "interrupted"
    StopError       StopReason = "error"
)

type StopContext struct {
    TurnSummary            // 嵌入现有 Message/ToolResults/Context
    Reason StopReason
    Turn   int
    Usage  models.TokenUsage // 本 turn 的 token 用量(预算记账数据源)
    LLM    *llm.Client       // 允许 hook 调模型(goal 评估、总结生成)
}

type ShouldContinueAfterStopFunc func(ctx context.Context, stop StopContext) (bool, error)
```

`terminate=true` 的硬停目前直接 break、不过 hook——与 Kimi 一致("hook-set stopTurn
wins over continuation"),保持,但 `AgentEndEvent.Reason` 已有区分,无需改。

### B. `max_turns_per_run` 硬上限 ★★★

loop 顶部检查,超限时以 `StopMaxTurns` 走**同一个** continuation 链(而不是直接
break)——这样 goal 预算 veto、steer 消化等逻辑对超限同样生效,与 Kimi 的
maxSteps → hook 路径一致。这也是未来 goal turn 预算(`turnBudget`)的落点。

### C. 单 hook → 有序 decider 链 ★★

Kimi 的优先级链是写死在一个闭包里的;Lcoder 装配更灵活,做成注册链:

```go
// 按注册顺序调用;第一个返回"停"的 decider 胜出,全部续跑才真正续跑。
// 注册顺序即优先级:goal 预算 veto 必须排在 steer 消化之前。
type ContinuationDecider func(ctx context.Context, stop StopContext) (cont bool, err error)
```

agenthost 装配时按需注册:goal budget veto → steering drain → (未来) 后台任务 drain →
外部 Stop hook。每个特性一个 decider,互不覆盖——当前单 `ShouldContinueAfterStopFunc`
只有一个属主,加第二个特性就会打架。

### D. `/goal` 指令:实现于 loop 之外 ★★

不动 loop。在 runner/agenthost 层加 `GoalDriver`,结构照搬 `driveGoal`:

```
/goal <text> [budgets]
  → GoalState{text, tokenBudget/turnBudget/wallClock} 注入 system reminder
  → while:
      a.Prompt(continuation) 跑一个普通 run
      run 结束(AgentEnd)后:
        模型已用 update_goal 工具标记 complete/give_up → 退出
        预算超 → 标记 blocked,退出
        否则 → Steer(GOAL_CONTINUATION_PROMPT),再 run
```

依赖关系:
- `update_goal` 内建工具(写 GoalState;OpAll access)——模型自己标记完成。
- 预算记账:订阅 `TurnEndEvent` 的 usage(A 的 `StopContext.Usage` 同一份数据)。
- 预算 veto:注册为 C 链上的 0 号 decider(goal active 且超预算 → 不许续跑)。
- TUI `/goal` 斜杠命令只是 GoalDriver 的启动入口。

### E. 明确不做

- **不把 goal 续跑塞进 loop 内**:Kimi 层 1 注释明确说 "Goal continuation is no
  longer driven here",早期版本在 hook 里硬撑 goal 续跑,后来重构到了 driver。
  教训已有人付过学费。
- **不恢复 followUp 队列**:steering + decider 链已覆盖其场景。
- **Kocoro 的 idle 看门狗 / force_stop**:daemon 场景需求,TUI 暂不需要;
  真到 daemon 模式时它是独立组件,不影响本设计。

### 落地顺序

| 步骤 | 内容 | 依赖 |
|:---:|------|------|
| 1 | `StopReason` + `StopContext`(A) | — |
| 2 | `max_turns_per_run`(B) | A |
| 3 | decider 链(C) | A |
| 4 | `update_goal` 工具 + `GoalDriver` + `/goal` 命令(D) | A+B+C |

