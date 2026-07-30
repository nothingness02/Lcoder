# Goal 模式与 Stop 机制完善 Spec

> 状态: 待评审(v2,经读者测试修订) | 前置分析: `docs/stop-conditions-comparison.md`(两层结构对比)
> 参考实现: Kimi Code `agent/turn/index.ts`(driveGoal)、`loop/run-turn.ts`(shouldContinueAfterStop)
>
> v2 修订(经无背景读者测试):max_turns 改为不过链的硬终局(消除再入问题);
> GoalDriver 改用 `Agent.LastEndReason()` 而非 bus 订阅取 endReason,分支表补
> terminated;goal veto decider 定为常驻链首 + status 自守卫(不做运行期增删);
> token 记账定为 run 循环 turn 边界的唯一写点(不经事件订阅)。

## 1. 背景与目标

Lcoder 当前的停止机制:`shouldStop`(默认"无 tool calls 即停")+ 单个
`ShouldContinueAfterStop` hook,hook 上下文 `TurnSummary` 只有
`{Message, ToolResults, Context}`。Kimi Code 用两层结构支持了 `/goal`:
turn 内 continuation hook(优先级链)+ turn 外 goal driver(while 循环跑普通 turn)。

本 spec 定义该结构到 Lcoder 的映射,覆盖四项改动(已与需求方确认):

| 项 | 内容 | 范围 |
|---|------|------|
| A | `StopReason` + `StopContext`(hook 上下文升级) | loop 内 |
| B | `max_turns_per_run` 硬上限 | loop 内 |
| C | 单 hook → 有序 `ContinuationDecider` 链 | loop 内 |
| D | Goal 模式:`GoalState` + `update_goal` 工具 + `GoalDriver` + `/goal` 命令 | loop 外 |

已确认的边界:**一份 spec 覆盖全部四项;`GoalDriver`/`GoalState` 放 `pkg/agent`;
第一版预算只支持 turn + token**(wall-clock 需要活跃计时器,后续再加)。

## 2. 概念映射总表

| Kimi Code | Lcoder 落点 | 文件 |
|-----------|------------|------|
| `shouldContinueAfterStop({stopReason, usage, stepNumber, llm})` | `ShouldContinueAfterStop(ctx, StopContext)` | `pkg/agent/loop.go` |
| `maxSteps`(`run-turn.ts:139` 循环顶 throw) | `Config.MaxTurnsPerRun`,run 循环顶检查 | `pkg/agent/loop.go` |
| turn/index.ts:884 手写优先级链 | `Config.ContinuationDeciders []ContinuationDecider` | `pkg/agent/loop.go` |
| `TurnFlow.driveGoal`(turn 外 while) | `GoalDriver.Run(ctx)` 循环调 `Agent.Prompt` | `pkg/agent/goal.go`(新) |
| goal record(status + budget) | `GoalState`,Agent 持有,入 checkpoint | `pkg/agent/goal.go`(新) |
| `UpdateGoal` 工具(模型自标完成) | `update_goal` 内建工具,`OpAll` | `pkg/tools/builtin/goal.go`(新) |
| goal 预算 veto(链 0 号位) | goal decider,goal active 时注册到链首 | `pkg/agent/goal.go` |
| `GOAL_CONTINUATION_PROMPT` / step-cap 变体 | 同名常量,经 `Agent.Steer` 注入 | `pkg/agent/goal.go` |
| `recordTokenUsage` 预算记账 | `GoalState.RecordUsage(models.LLMUsage)` | `pkg/agent/goal.go` |
| `/goal` TUI 命令族 | `/goal` 斜杠命令(仿 `/compact`) | `pkg/tui/goal.go`(新) |
| goal record 持久化(resume paused goals) | `checkpoint.AgentSnapshot.Goal` | `pkg/checkpoint/checkpoint.go` |
| turn 失败事件区分 `max_steps` | `events.EndReasonMaxTurns` | `pkg/events/types.go` |

刻意不映射:wall-clock 预算、goal deadline 调度器(Kimi v2 的
`goalDeadlineScheduler`)、外部 Stop hook(用户脚本)、print 模式后台任务
drain。均为后续增量。

## 3. A:StopReason 与 StopContext

`pkg/agent/loop.go`:

```go
// StopReason classifies why the loop is about to stop. Mirrors Kimi Code's
// terminal stop reasons; tool_use never reaches the continuation chain.
type StopReason string

const (
    StopEndTurn     StopReason = "end_turn"     // 无 tool calls,自然完成
    StopTerminated  StopReason = "terminated"   // 工具 terminate 标记
    StopMaxTurns    StopReason = "max_turns"    // MaxTurnsPerRun 硬上限
    StopInterrupted StopReason = "interrupted"  // abort / 用户中断
    StopError       StopReason = "error"        // stream 或执行错误
)
```

**目前只有 `StopEndTurn` 会到达 continuation 链**;terminated / max_turns /
interrupted / error 都是不过链的硬终局(§4、现状一致)。完整枚举是为
`StopContext` 的分类能力与后续演进,不是因为每条都会喂给 decider。

// StopContext is the continuation decision's input. It supersedes TurnSummary
// for ShouldContinueAfterStop: the hook can now tell WHY the loop is stopping,
// how far it got, and call the LLM (e.g. goal 完成度评估、总结生成).
type StopContext struct {
    TurnSummary           // 嵌入: Message / ToolResults / Context
    Reason StopReason
    Turn   int
    Usage  models.LLMUsage // 本 turn 的 token 用量;无则为零值
    LLM    *llm.Client
}

type ShouldContinueAfterStopFunc func(ctx context.Context, stop StopContext) (bool, error)
```

要点:
- `TurnSummary` 保留(`ShouldStopFunc` 的入参不变,它是 per-turn 判断,不需要
  stop 语义);`StopContext` 嵌入它,不破坏现有测试构造方式。`StopContext` 是
  步骤 1(单 hook 签名升级)与步骤 3(decider 链)共用的入参类型——§5 的
  `ContinuationDecider` 与这里的 `ShouldContinueAfterStopFunc` 入参一致,
  步骤 3 只替换字段形态,不再动签名。
- `Usage` 的来源:streamer 已有每响应的 `models.LLMUsage`(喂给
  `contextmgr.RecordRealUsage`);run 循环在 turn 结束点顺手缓存最后一份传入
  StopContext(**只读**,供 decider 判断;预算记账的唯一写点见 §6.1),并
  **同步加到 `TurnEndEvent`**(展示与可观测性用途,不作为记账源)。
- 中断/错误路径目前直接 break、不过 hook(与 Kimi "abort 即终局"一致),保持。
  注意 `StopReason` 与既有 `AgentEndReason` 是两套词汇:`StopEndTurn` 对应
  `EndReasonCompleted`("end_turn" 是 Kimi 用词,event 侧不改名),
  `StopInterrupted/StopError` 与 `interrupted/error` 一致,`max_turns` 新增见 §4。

## 4. B:max_turns_per_run

```go
// Config( pkg/agent/loop.go )
// MaxTurnsPerRun caps the number of provider turns in one Prompt run.
// 0 means unlimited. Exceeding it ends the run IMMEDIATELY with
// EndReasonMaxTurns — it does NOT pass the continuation chain (mirrors
// Kimi's maxSteps throw: a hard, one-shot terminal condition).
MaxTurnsPerRun int
```

- 检查点:`run()` 的 for 循环顶部(`turn > MaxTurnsPerRun` 时置
  `endReason = EndReasonMaxTurns` 并 break),位置对应 Kimi `run-turn.ts:139`。
- **超限是硬终局,不过 continuation 链**——与 Kimi 的 throw 语义一致。若允许
  decider 续跑,循环顶部会立即再次触发,产生"每转一圈过一次链"的再入问题;
  硬终局同时保证 `AgentEndEvent.Reason` 恒为 `max_turns`,GoalDriver 的判定
  依据无歧义。goal 的跨 turn 追求在 driver 层恢复(driver 看到 max_turns 就
  再开一个新 run),不需要 loop 内容许例外。
- `events.AgentEndReason` 新增 `EndReasonMaxTurns = "max_turns"`,等价 Kimi 的
  `isMaxStepsTurnFailure`:GoalDriver 靠它区分"撞上限"(继续)与"真错误"(暂停)。
- run 结束时把 `endReason` 写入 agent 状态并暴露 `Agent.LastEndReason()`
  ——`Prompt` 串行返回后调用它读取,无时序竞争(GoalDriver 不依赖 bus
  订阅拿 endReason,见 §6.3)。
- 默认值:0(不限)。goal 模式下 GoalDriver 建议值(如 50)由装配方传入,
  不写死。
- checkpoint:`AgentSnapshot` 加 `MaxTurnsPerRun`(与 Mode/Model 同级,参与
  `agentConfigHash`)。

## 5. C:ContinuationDecider 链

```go
// ContinuationDecider decides whether the loop continues after a stop signal.
// Deciders run in registration order; the FIRST decider that returns
// (false, _) or (_, err) wins and the loop stops. All returning true means
// continue. Registration order IS the priority: hard vetoes (goal budget)
// must register before drains (steering) and soft continuations.
type ContinuationDecider func(ctx context.Context, stop StopContext) (bool, error)

// Config
ContinuationDeciders []ContinuationDecider
```

- run 循环中的调用点不变(现 `ShouldContinueAfterStop` 处),改为按序遍历链。
- 兼容:`ShouldContinueAfterStop` 字段保留一个版本周期?——**不保留**(项目
  约定不做向后兼容)。直接删除,装配点(cmd/lcoder/main.go、agenthost、
  测试)迁移为注册单个 decider。
- 链为空的默认行为 = 现状(nil hook):停。
- Kimi 把顺序写死在一个闭包里;链式注册是等价的显式版本,每个特性
  (goal veto / steer drain / 未来的后台任务 drain)一个 decider,互不覆盖。
- **链在装配期定型,运行期不做增删。** goal 预算 veto 不需要"goal active 时
  动态注册"——它由 agent 在 `New()` 里内置为链首(见 §6.3),goal 为 nil 或
  非 active 时自守卫放行,避免运行期改 `a.cfg` 的并发问题。

## 6. D:Goal 模式

### 6.1 GoalState(`pkg/agent/goal.go`)

```go
// GoalStatus mirrors Kimi's goal record lifecycle.
type GoalStatus string

const (
    GoalActive   GoalStatus = "active"   // driver 正在追求
    GoalPaused   GoalStatus = "paused"   // 中断/错误,可 resume
    GoalBlocked  GoalStatus = "blocked"  // 预算耗尽或模型判死局,可 resume
    GoalComplete GoalStatus = "complete" // 模型经 update_goal 自标完成
)

// GoalState is the agent-held goal record. The model mutates it ONLY through
// the update_goal tool (applied by the executor); the driver and /goal
// commands mutate it directly.
type GoalState struct {
    Objective   string
    Status      GoalStatus
    TurnBudget  int // 0 = 不限;goal 模式总 turn 预算
    TokenBudget int // 0 = 不限;累计 output token 预算
    TurnsUsed   int
    TokensUsed  int
    BlockReason string // blocked/paused 时的原因,展示用
}

func (g *GoalState) OverBudget() bool
func (g *GoalState) RecordUsage(u models.LLMUsage) // TokensUsed += u.CompletionTokens
```

- Agent 持有 `goal *GoalState`(nil = 无 goal),`Agent.Goal()` 只读暴露。
- **记账唯一写点**:`run()` 循环在 turn 边界(TurnEnd 之后)若 goal active
  则 `goal.RecordUsage(turnUsage)`。不经事件订阅、不经 decider,杜绝双记账;
  `TurnEndEvent.Usage` 仅供 TUI/日志展示,`StopContext.Usage` 只读。
- 预算只计 **output token**(`CompletionTokens`,与 Kimi
  `recordTokenUsage(usage.output)` 一致,input 重复计费会放大幻觉;output
  是模型"工作量"的稳定代理)。

### 6.2 `update_goal` 工具(`pkg/tools/builtin/goal.go`)

仿 `todo.go` 的 inert-tool 模式:工具本身只校验参数并回显,**状态迁移由
executor 在拿到结果后应用**(与 `task.ToolName` reconcile 同构)。

```
参数:
  status: "complete" | "blocked"   (必填)
  reason: string                   (blocked 时必填,complete 时可选总结)
```

- **不实现** `AccessDeclarer`:遵循代码库约定,未声明即默认 `OpAll`(写
  agent 共享状态,与一切串行),与 `todo_write`/`use_skill`/`subagent` 一致。
- executor 应用规则:`active → complete/blocked` 合法;其他迁移返回错误
  tool_result(模型可自纠正)。迁移后 emit `GoalUpdatedEvent`。
- 工具的 description 直接移植 Kimi `GOAL_CONTINUATION_PROMPT` 中的
  completion/blocked audit 语义(complete 前必须验证真实状态;blocked 需
  同一阻塞持续 3 个 goal turn 等),这是防止模型过早收工的关键 prompt 资产。

### 6.3 GoalDriver(`pkg/agent/goal.go`)

`driveGoal` 的忠实映射,**不碰 run 循环**:

```go
// GoalDriver runs ordinary Prompt turns until the goal settles. It is the
// loop-external half of Kimi's two-layer design: per-turn safety (max_turns)
// stays inside the run; cross-turn pursuit lives here.
type GoalDriver struct {
    agent *Agent
}

func (d *GoalDriver) Run(ctx context.Context, objective string, budget GoalBudget) error
```

驱动逻辑(对照 `driveGoal` 逐条):

```
创建 GoalState{active} → 注入 goal system reminder(经 ephemeral reminder,不写死 system block)
loop:
    goal.OverBudget() → 置 blocked("budget reached"),返回
    goal.TurnsUsed++
    err := agent.Prompt(ctx, nextMsg)     // 首个 turn 是用户原始输入,之后是 continuation prompt
    按 agent.LastEndReason() 分支(Prompt 串行返回即同步,不经 bus 订阅):
      interrupted        → 置 paused("interrupted"),返回
      error              → 置 paused(err),返回           // 对应 Kimi failed→pause
      max_turns          → 落 continue(下轮用 STEP_CAP 变体 prompt)
      completed/terminated → 落下方重读 goal              // terminated 是模型显式硬停,同样尊重
    重读 goal:
      status != active   → 返回(模型已用 update_goal 决出 complete/blocked)
      OverBudget()       → 置 blocked,返回
    nextMsg = GOAL_CONTINUATION_PROMPT(或 max_turns 时的 STEP_CAP 变体),经 Steer 注入
```

- 两个 prompt 常量直接移植 Kimi 文本(含 step-cap 变体),这是经过验证的
  prompt 资产,不重写。
- GoalDriver 与 TUI 的并发:driver 在自己的 goroutine 里串行调 `Prompt`
  (Prompt 本身阻塞至 run 结束);用户中途输入走现有 `Steer`,中断走 `Abort`。
- **goal budget veto decider**:agent 在 `New()` 里把一个内置 decider 放到链首
  (在 `Config.ContinuationDeciders` 之前),闭包读 `a.goal`:
  `goal != nil && goal.Status == GoalActive && goal.OverBudget() → (false, nil)`。
  常驻 + status 自守卫,goal 终态后自然失效,无运行期增删。对应 Kimi 层 1
  的 0 号位——确定性天花板,veto 一切续跑。

### 6.4 `/goal` TUI 命令(`pkg/tui/goal.go`,仿 `compact.go`)

| 命令 | 行为 |
|------|------|
| `/goal <objective>` | 创建 goal(可选 `--turns=N --tokens=M`),启动 GoalDriver |
| `/goal status` | 展示 objective/status/预算用量 |
| `/goal pause` | 置 paused(当前 run 到边界后 driver 退出) |
| `/goal resume` | paused/blocked → active,重启 driver |
| `/goal cancel` | 清除 goal(等价 Kimi 的清除 record) |

goal 状态展示走 footer(仿 Kimi 的 goal-panel,第一版只做一行状态文本)。

### 6.5 持久化

- `checkpoint.AgentSnapshot` 加 `Goal *GoalState`(omitempty)。checkpoint 本就
  per-turn 写,goal 崩溃恢复自然获得;`Agent.Restore` 恢复 goal 时若状态为
  active 一律降级为 **paused**(崩溃时 run 一定不在安全点,不自动续跑),
  由用户 `/goal resume` 显式继续——对齐 Kimi "blocked/paused is resumable"
  的保守语义。
- 预算用量(TurnsUsed/TokensUsed)随 GoalState 一起持久化,不单独记账。

## 7. 事件与可观测性

| 事件 | 改动 |
|------|------|
| `TurnEndEvent` | 加 `Usage models.LLMUsage`(展示/可观测性;**不是**记账源,记账写点在 run 循环,见 §6.1) |
| `AgentEndReason` | 加 `EndReasonMaxTurns`;run 结束写入 agent 状态,经 `Agent.LastEndReason()` 暴露 |
| `GoalUpdatedEvent`(新) | status/objective/预算用量;TUI footer 与会话日志订阅 |

`TurnSummary`(ShouldStop 入参)不变。

## 8. 落地顺序与验收

| 步骤 | 内容 | 验收 |
|:---:|------|------|
| 1 | StopReason + StopContext(A) | 现有 hook 测试迁移后全绿;hook 能读到 Reason/Turn/Usage/LLM |
| 2 | MaxTurnsPerRun + EndReasonMaxTurns(B) | 超限测试:run 以 max_turns 硬结束(不过链);`LastEndReason()` 可读出;driver 视角是干净边界 |
| 3 | ContinuationDecider 链(C) | 顺序语义测试(首个 false 胜出);装配点迁移完 |
| 4 | GoalState + update_goal + GoalDriver + /goal(D) | 集成测试(llmtest 脚本化):模型两 turn 后 update_goal complete → driver 退出且状态 complete;预算耗尽 → blocked;abort → paused |

每步独立可合,1→2→3 是 loop 内小改,4 依赖 1+2+3。

## 9. 明确不做(Out of Scope)

- wall-clock 预算与 goal deadline 调度器(Kimi v2 `goalDeadlineScheduler`)
- 外部 Stop hook(用户脚本,对应 Claude Code hooks)
- print/one-shot 模式的后台任务 drain(Kimi 层 1 的 1.5 号位)
- goal 的 system prompt 模板化(第一版用固定 reminder 文本)
- 子代理 goal(Kimi v2 明确拒绝 `reject subagent goals`,Lcoder 同样不支持)
