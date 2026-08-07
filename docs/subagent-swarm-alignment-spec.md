# Subagent / Swarm 对齐 kimi-code v2 规格说明书

> 对齐目标：`reference/kimi-code/packages/agent-core-v2/src` 的 subagent 与 swarm 机制。
> 对齐原则：对齐"能力"而非"架构"——lcoder 用扁平 `agent.New` + journal，不复制 kimi 的 DI scope 容器。
> 关联文档：`docs/data-loop-audit.md`（事件闭环审计）、`docs/rpc-protocol.md`（协议面）。

## 一、背景与目标

### 1. 现状差距摘要

| 维度 | kimi-code v2 | lcoder 现状 | 差距 |
|---|---|---|---|
| 工具形态 | `Agent` + `AgentSwarm` 独立工具 | 单一 `subagent` 工具参数分流 | 中 |
| 并发调度 | `AgentRunBatch`：burst ramp + 限流感知 + 容量收缩/恢复 | `errgroup.SetLimit(4)` 固定 | **大** |
| 限流恢复 | 指数退避 + 重排队 + `subagent.suspended` 事件 | 固定 3s 后 `Resume()` 一次 | **大** |
| resume 混合 | `items` + `resume_agent_ids` 同批 | swarm 仅 items | 中 |
| swarm 模式 | `SwarmModel` 状态机 + enter/exit reminder | 无 | 中 |
| 模型绑定 | tool → profile → secondary → 父，catalog 校验 | profile.Model 或 inherit | 中 |
| allowlist | 工具调用时显式校验 | 仅注册层 | 小 |
| 后台任务 | TaskList/TaskOutput/TaskStop 工具族 | bgResults + reminder | 中 |
| 归属元数据 | `parentAgentId` + `swarmItem` 持久化 | session 级 ParentSessionID，无 swarmItem | 小 |
| 批内并行 | 同消息多 `Agent` 调用并行（`Promise.allSettled`） | `subagent` 无 `DeclareAccesses` → 默认 `OpAll` **串行** | **大** |

### 2. 对齐目标

1. 调度正确性优先：ramp 防突发 + 限流感知恢复 + 批量取消；
2. 工具形态对齐：独立 `AgentSwarm` 工具 + `resume_agent_ids` 混合；
3. 通信闭环：suspended 事件、swarmItem 归属、resume relabel；
4. 能力对齐不架构对齐：不引入 DI scope。

---

## 二、总纲：机制映射

| kimi-code v2 | lcoder 对应物 | 对齐动作 |
|---|---|---|
| `Agent` + `AgentSwarm` 工具 | `builtin/subagent.go` | 拆分独立 `AgentSwarm` 工具 |
| `AgentRunBatch` | `runSwarm()` errgroup | 新 `pkg/subagent/batch.go` |
| `SwarmModel` 状态机 | 无（仅 executor 独占 veto） | 复用 ephemeral reminder |
| `applyProfilePromptPrefix` + item 描述 | `rolePrefixText + profile.Prompt` | 补 swarm item 描述/index 注入 |
| `lifecycle.create` + model catalog | `buildChild()` + `resolveModel` | 补 per-subagent model 绑定 |
| `Permissions.Fork` + allowlist | `Permissions.Fork()` + `profile.Subagents` | 补显式 allowlist 校验 |
| `mirrorAgentRun` + wire 信号 | `mirrorChild` + `SubagentActivityEvent` | 补 suspended 事件、swarmItem 归属 |

---

## 三、详细设计

### 3.1 工具面：拆分独立 `AgentSwarm` 工具

**现状**：`pkg/tools/builtin/subagent.go` 单一工具，`prompt_template+items` 参数分流。

**目标设计**：

1. 新建 `pkg/tools/builtin/swarm.go`，独立工具 `AgentSwarm`，schema：

```
subagent_type       // 默认 "coder"
prompt_template     // 必含 {{item}}
items               // ≥2 ≤128，展开去重
resume_agent_ids    // map<agent_id, prompt>，与 items 可混合
description
model               // per-subagent 模型绑定（可选）
```

2. `subagent` 工具保留单任务 / 后台 / resume 形态，但新增 `resume_agent_ids` 混合能力（对齐 kimi `createAgentSwarmSpecs`：`resumeCount > 0 || itemCount >= 2` 才放行；`totalCount ≤ 128`）。

3. 工具描述沿用 kimi `agent-swarm.md` 文案结构：明确 "must be the ONLY tool call" + `resume_hint` 指引。

4. executor 独占 veto 保留，文案拆两类（对齐 `swarmService.ts`）：
   - 多个 `AgentSwarm` 同批：*"issue them sequentially: call one AgentSwarm, wait for its result, then call the next"*；
   - `AgentSwarm` 混批：*"must be the only tool call in a model response"*。

### 3.2 调度机制：移植 `AgentRunBatch`

**现状**：`runSwarm()` `errgroup.SetLimit(4)`；失败固定 3s `Resume()` 一次。

**目标设计**：新建 `pkg/subagent/batch.go`，Go 版 `agentRunBatch`：

```go
// pkg/subagent/batch.go —— 新接口
type BatchTask struct {
    Profile   subagent.Agent
    Prompt    string
    SwarmItem string            // resume 时 relabel 用
    ResumeID  string            // 非空 = resume 形态
    Timeout   time.Duration
}

type RunBatch struct {
    Spawn          func(ctx context.Context, t BatchTask) *subagent.Outcome
    Resume         func(ctx context.Context, agentID, prompt string) *subagent.Outcome
    Suspend        func(agentID, reason string)          // → bus 事件
    MaxConcurrency int                                   // env 可配：LC_AGENT_SWARM_MAX_CONCURRENCY
}

// 常量（对齐 agentRunBatch.ts）
const (
    initialLaunchLimit      = 5               // 前 5 个立即启动
    initialLaunchInterval   = 700 * ms        // 之后每 700ms 放行 1 个
    rateLimitRetryBase      = 3 * time.Second // 指数退避基数
    rateLimitRetryFactor    = 2
    capacityShrinkInterval  = 2 * time.Second // 限流中每 2s 容量 -1（floor 1）
    capacityRecoveryInterval = 3 * time.Minute // 每 3min 容量 +1
)
```

**状态机**：

```
schedule()
 ├─ 正常模式：while normalLaunchCount < 5 && pending 非空 && !限流 && 未达并发上限 → startAttempt
 │            （每启动一个，间隔 700ms）
 ├─ 限流模式（任一 attempt 捕获 rate-limit 错误后进入）：
 │    ├─ requeueRateLimited：该任务重新入队（pending 头），retryCount+1
 │    │    retryReadyAt = now + 3s × 2^retryCount
 │    │    Suspend(agentID, "Provider rate limit; subagent requeued for retry.")
 │    ├─ rateLimitCapacity = max(1, startedSuccessCount)  → 每 2s -1 → 每 3min +1
 │    ├─ globalRetryIntervalMs：连续失败时翻倍，限制整体放行速率
 │    └─ retry 复用 journal：launcher.Retry(agentID) → Host.Resume（保留部分进度）
 └─ 收尾：batch 级 context.WithCancel，cancel() 广播全部 in-flight
        结果状态机：completed / failed / aborted(+state: started/not_started)
        每任务独立 timeout
```

**前置依赖（P0 第一项）**：llm 层暴露结构化错误判定 `IsProviderRateLimitError(err)`（解析 429 / `overloaded_error` 等），对齐 kimi `isProviderRateLimitError`。整个限流模式以此为触发条件。

### 3.3 提示词构建

**现状**：`buildChild()` `BaseSystemPrompt = rolePrefixText() + "\n\n" + profile.Prompt`；swarm item 无 index/描述；无 origin 标记。

**目标设计**：

1. **swarm item 上下文注入**：`SpawnRequest` 带 `SwarmIndex`/`SwarmTotal`/`SwarmDescription` 时，在 BaseSystemPrompt 前追加：

```go
fmt.Sprintf("\n\nYou are handling swarm item %d/%d of %q (subagent_type: %s).",
    index, total, description, profileName)
```

对齐 kimi `childDescription`（`${desc} #${index} (${profile})`）。

2. **origin 标记**：子 agent 的 user 消息 metadata 加 `origin: "subagent"`（对齐 `AGENT_RUN_PROMPT_ORIGIN`），供事件/审计区分消息来源。

3. **摘要策略**：保留 `distillSummary` + `SummaryMinChars/Retries`，profile YAML 可加 `summary_policy: brief|detailed`（对齐 kimi `summaryPolicy`）。

### 3.4 权限设置

**现状**：`Permissions.Fork()`（共享规则+私有 guard）+ `SetPathContext(cwd)` + `profile.Mode` + `UserConfirm` 共享。

**目标设计**：

1. **显式 allowlist 校验**：`Subagent.Execute`/`AgentSwarm` 工具内，读父 profile 的 `Subagents` 字段校验 `subagent_type`，未知类型返回 kimi 风格错误 `subagentTypeNotAllowedMessage`——与注册层（`Without("subagent")`）双保险。

2. **模式继承选项**：profile 支持 `mode: inherit`（显式继承父当前模式），默认仍是 profile.Mode。

3. **独立审批规则名**：两个工具各自声明 `approvalRule`（`subagent` / `subagent_swarm`），供权限引擎按工具名配置 allow/deny。

### 3.5 Agent 搭建（生命周期与模型绑定）

**现状**：`buildChild()` 直接 `agent.New`；`resolveModel` 只支持 `profile.Model` 或 `"inherit"`；无 per-subagent model 参数、无 catalog 校验。

**目标设计**：

1. `SpawnRequest` 增加 `Model`/`Thinking` 字段（`pkg/subagent/spawn.go`）；工具参数 `model` 优先，`resolveModel` 三档：`tool.Model → profile.Model → parent`。

2. 模型经 `h.cfg.LLMClient` catalog 校验存在性，失败返回 wrap 风格错误。

3. **实例 registry**：`Host` 加 `agents map[agentID]*agent.Agent`（journalStore 旁），对齐 kimi `lifecycle.get(agentId)`——供 resume 时 idle 校验（当前 `journal.markRunning` 无法区分"进程内活跃实例"与"仅 journal 存在"）。

### 3.6 通信机制

#### 结果回传
- 对齐 `<resume_hint>` 结构（有失败时提示用 `resume_agent_ids` 续跑）+ `mode="resume"` 属性（resume 的 subagent 上标记）。

#### 活动镜像（UI 嵌套）
- `mirrorChild` 已对齐 `mirrorAgentRun` 主链（SubagentActivityEvent → TUI 嵌套显示）。
- **新增**：`SubagentSuspendedEvent{AgentID, Reason}`（`pkg/events/types.go`），TUI 嵌套显示渲染 `(suspended: rate limit)`。

#### 后台任务管理
- 低配：`/tasks` 侧边栏扩展一行"后台子 agent"条目（对齐 kimi TaskList 的最小集）。
- 完整（P2 可选）：TaskOutput/TaskStop 等价工具。

#### 取消与归属
- journal meta（`pkg/agenthost/journal.go` 的 `journalMeta`）增加 `SwarmItem`/`ParentToolCallID` 字段；
- swarm resume 时用 `getSwarmItem` 等价逻辑还原 item 标签；
- 批量取消对齐 batch 级 `AbortController`。

### 3.7-bis 批内并行：异构任务的并行通道（P1，✅ 已实施）

**现状（关键差异）**：`subagent` 工具**未实现 `DeclareAccesses`**（`subagent.go` 无 `AccessDeclarer`），executor 默认 `{Op: OpAll}`（executor.go:353）→ `AccessesConflict(OpAll, ·) = true` → **同一消息里的多个独立 `subagent` 调用被 batchScheduler 强制串行**。

kimi 的 toolExecutor 对同一响应里所有工具调用执行 `Promise.allSettled`（toolExecutorService.ts:483）——**多 `Agent` 调用天然并行**，异构任务用独立调用即可。lcoder 因此缺了 kimi 的完整光谱中"异构任务并行"这一档：swarm 要求同构模板，独立调用串行，模型没有并行跑两个异构子 agent 的通道。

**目标设计**：给 `subagent` 工具实现 `AccessDeclarer`，声明"不触碰共享资源"的 access，使同批多个独立调用并行（对齐 kimi `Promise.allSettled`）：

```go
// pkg/tools/access.go 新增操作符
OpNone  AccessOperation = "none"   // 不触碰任何资源；仅与 OpAll 冲突

// pkg/tools/builtin/subagent.go
func (s *Subagent) DeclareAccesses(args map[string]any) []tools.ToolAccess {
    // 子 agent 在自己的进程/cwd 上下文运行，父侧无确定路径；
    // 声明 OpNone 使同批独立 subagent 调用互不冲突 → 可并行。
    // （并发写入同一文件的风险由父模型按 kimi 指引避免分配冲突职责）
    return []tools.ToolAccess{{Op: tools.OpNone}}
}
```

**冲突判定扩展**（`access.go:50 AccessConflict`）：`OpNone` 与任何非 `OpAll` 不冲突、两个 `OpNone` 不冲突，仅与 `OpAll` 冲突。

**注意**：swarm 独占 veto（executor.go:265）与 OpNone 正交——swarm 形态（items）仍需批内唯一，只有非 swarm 的独立 `subagent` 调用受益于 OpNone 并行。

### 3.7 Swarm 模式状态机（P2，可选）

复用 `pkg/contextmgr/ephemeral.go` 的 reminder 基础设施：

1. `AgentSwarm` 工具执行时注入 swarm_mode enter reminder（*"你已进入 swarm 模式，等待 AgentSwarm 结果，不要继续发散工具调用"*）；
2. 结果回来后清除（pop 或注入 exit reminder）；
3. 保持 ephemeral 语义（live-only，不进块不落盘）；
4. 对齐 kimi `SwarmModel` trigger 语义（tool/task 触发 → turn.ended 自动退出）。

---

## 四、分期实施计划

> 状态图例：✅ 已完成 · 🔜 进行中 · ⏸ 已决定暂缓 · ⬜ 未开始

| 阶段 | 内容 | 涉及文件 | 验收 | 状态 |
|---|---|---|---|---|
| **P0** | llm 层 `IsRateLimited`（结构化限流判定） | `pkg/llm/retry.go` | 429/overloaded 判定单测 | ✅ `IsRateLimited`+`RateLimitRetryAfter` 落地（复用 `EventError.Code=="rate_limit"`） |
| **P0** | `pkg/subagent/batch.go`：ramp + 限流退避 + 重排队 + 批量取消 + suspended 事件 | `pkg/subagent/batch.go`、`pkg/events/types.go` | fake launcher 三阶段单测 | ✅ 10 测试全绿；`SubagentSuspendedEvent` 落地并 wire 到 main.go |
| **P1** | 拆独立 `AgentSwarm` 工具 + `resume_agent_ids` 混合 | `pkg/tools/builtin/swarm.go`、`subagent.go` | 混批/去重/上限校验测试 | ✅ `subagent_swarm` 独立工具 + veto 双文案 + 双注册 + 混合 resume |
| **P1** | journal meta 补 `SwarmItem` + resume relabel | `pkg/agenthost/journal.go` | resume 还原 item 标签测试 | ✅ `journalMeta.SwarmItem` 持久化 + `Host.SwarmItemOf` + 跨重启读回测试 |
| **P1** | **批内并行：`subagent` 实现 `DeclareAccesses`（OpNone）+ 冲突判定扩展** | `pkg/tools/builtin/subagent.go`、`pkg/tools/access.go` | 同批两个独立 subagent 调用并行执行 | ✅ `OpNone` + `AccessConflict` 扩展 + executor 并行测试 |
| **P1** | per-subagent model 绑定 + catalog 校验 | `pkg/subagent/spawn.go`、`host.go` | 三档绑定优先级测试 | ⏸ **已决定暂缓**：纯增量能力，与其余改动正交；后续补时在 `SpawnRequest`/`journalMeta` 叠加 `Model` 字段即可 |
| **P2** | swarm 上下文 reminder | `pkg/contextmgr/ephemeral.go` | 注入/退出/不落盘测试 | ⬜ |
| **P2** | 显式 allowlist 校验 + 独立审批规则名 | `pkg/tools/builtin/subagent.go`、`permissions` | 拒绝消息测试 | ⬜ |
| **P2** | 外部 hooks `SubagentStart/Stop`、后台任务进 `/tasks` | `pkg/agenthost/host.go`、`pkg/tui` | 钩子触发测试 | ⬜ |
| **P2** | TUI 渲染 `SubagentSuspendedEvent`（嵌套显示 `(suspended: rate limit)`） | `pkg/tui` | 状态显示 | ⬜ 事件已可发，UI 消费待做 |

---

## 五、关键取舍与风险

1. **限流感知的地基是结构化错误**：llm 层当前把 provider 错误统一成 `error`，`IsProviderRateLimitError` 必须先落地，否则限流模式无法触发——P0 第一项。✅ 已落地（`IsRateLimited` 复用 `EventError.Code=="rate_limit"`，由 adapter.go:120 / anthropic.go:168 分类产出）。
2. **不复制 DI scope**：kimi 的 `lifecycle.create` 基于完整 DI 容器，lcoder 用 `agents map` + journal 校验达到同等能力即可，避免过度工程。
3. **swarm 模式状态机的优先级**：价值在于防止模型在 swarm 等待期间继续发散，低于调度正确性（P0/P1），放 P2。
4. **调度器可测性**：batch 调度器是纯逻辑（kimi 也是纯类），用 fake launcher 做单元测试覆盖 ramp / 限流 / 取消三阶段。
5. **OpNone 的语义边界**：`OpNone` 使子 agent 调用与所有非 OpAll 工具并行——子 agent 可能写文件，与同批的 `edit` 等写工具并行存在理论竞态；对齐 kimi 的取舍（`Promise.allSettled` 无条件并行 + 指引模型避免冲突职责），并发正确性由父模型负责，调度器只保证无确定资源冲突。

---

## 六、验证与验收

1. `go test ./... -count=1 -race` 全绿；✅（全量测试通过）
2. batch 调度器单测覆盖：burst ramp 时序、限流进入/容量收缩/恢复、重排队退避、批量取消（started/not_started 区分）；✅（`pkg/subagent/batch_test.go` 10 测试）
3. 手工验证：
   - 10 个 items 的 swarm：前 5 个立即启动，之后逐个放行；⬜ 待手工验证（ramp 时序已有单测覆盖）
   - 模拟 provider 429：任务重排队、3s×2^n 退避、容量逐级收缩、最终全部完成；✅（单测覆盖）
   - `resume_agent_ids` + items 混合：失败项续跑且标签（item）正确还原；✅（swarm 混合测试 + SwarmItem 持久化测试）
   - 取消：用户中断时已开始/未开始任务状态区分正确，journal 可续跑；✅（单测覆盖）
   - **批内并行**：同一消息发两个独立 `subagent` 调用（非 swarm），验证并发执行；✅（`executor_parallel_test.go`）
4. TUI：子 agent 嵌套显示含 suspended 状态；`/tasks` 面板显示后台子 agent；⬜（事件已可发，UI 消费待做）

## 七、一句话总结

**以调度正确性为第一优先级**（P0 结构化错误 + AgentRunBatch 移植），**工具形态与通信闭环为第二优先级**（P1 独立 AgentSwarm + 混合 resume + 归属元数据），**swarm 模式状态机与后台管理为第三优先级**（P2）——全程对齐 kimi 的能力语义而不复制其 DI 架构。
