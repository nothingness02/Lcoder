# Lcoder 系统现状评估报告

> 生成日期：2026-07-01
> 范围：Lcoder 代码库整体架构、实现完整性、测试覆盖、依赖与演进方向
> 方法：代码扫描、`go test ./...`、`go vet ./...`、依赖分析、文档与实现对比

---

## 一、重要前提：现有设计问题文档已过时

`docs/agent-design-issues.md` 中列出的多数问题**已经解决或部分解决**。若按该文档直接推进，会误判当前真实状态。

| 原文档问题 | 当前状态 | 说明 |
|-----------|---------|------|
| `static_ratio` 未落地 | ✅ 已解决 | `pkg/contextmgr/window.go` 已实现 `enforceStaticRatio` |
| `CacheHint` 未使用 | ✅ 已解决 | `project_docs`/`skills` 已设置 `CacheHintBreakpoint`，`BuildTurnRequest` 已消费 |
| `tool_search` 不完整 | ✅ 已解决 | `executor.go` 返回完整 schema，并维护 `activeDeferred` 提升工具 |
| `TransformContext` 绕过上下文管理 | ✅ 已解决 | 现在 `TransformContext` 在 `BuildTurnRequest` 之后应用，并重新计算 cache breakpoints 和 max_tokens |
| 上下文压缩双路径 | ✅ 已解决 | `MaybeCompact` 已委托给 `MaybeCompactLeveled` |
| `GatewayEvent` 类型安全弱 | ✅ 已解决 | `GatewayEvent` 现在是强类型的 `provider.Event` 包装 |
| 事件错误被忽略 | ✅ 已解决 | `events.Bus.Emit` 返回错误，`eventEmitter` 会记录到 observability 或 stderr |
| 权限 `ask` 在 CLI/TUI 不一致 | ✅ 已解决 | 已抽象为 `UserConfirmation` 接口，CLI 和 TUI 分别实现 |
| Agent 核心职责过重 | ⚠️ 部分改善 | `streamer` 已拆出，但 `Agent` 仍承担较多职责 |

**建议**：更新或归档 `docs/agent-design-issues.md`，避免误导后续开发者。

---

## 二、当前缺陷与待改进点

### 1. 依赖冗余：Shannon 依赖未使用

**问题**
`go.mod` 引入了 `github.com/Kocoro-lab/Shannon/go/orchestrator`，连带引入 Temporal SDK、Redis 客户端、gRPC、OpenTelemetry、Prometheus 等大量依赖。但主代码中没有任何使用 `Shannon` 的引用。

**影响**
- 编译时间显著变长
- 二进制体积变大
- 依赖安全漏洞管理负担增加
- 新开发者困惑

**建议**
- 移除 `github.com/Kocoro-lab/Shannon/go/orchestrator` 依赖
- 运行 `go mod tidy` 清理间接依赖
- 如无明确使用计划，不应保留重型未用依赖

---

### 2. Checkpoint 与 Session 重复存储

**问题**
- Session：`~/.lcoder/sessions/<hash>/<id>.jsonl`，保存所有消息
- Checkpoint：`~/.lcoder/sessions/checkpoints/<id>.checkpoint.json`，保存完整运行时快照
- Checkpoint 的 `Context.Blocks` 中也保存了所有消息
- 两者都频繁写盘
- Checkpoint 为单文件覆盖，无历史版本

**影响**
- 消息内容重复保存
- 每轮都重写整个 checkpoint JSON，写放大严重
- 长会话下磁盘 I/O 和存储空间开销大

**建议**
- 降低自动 checkpoint 频率，或提供配置开关
- 支持多版本 checkpoint
- 或让 checkpoint 只保存运行时状态（budget、mode、turn、queues、deferred 等），消息从 session 加载

---

### 3. Sandbox 后端不完整

**问题**
- `container` / `remote` 后端仅返回 `not yet implemented` 错误
- Windows 下 `soft-limit` 的进程组隔离和 rlimit 未实现（`exec_windows.go` 是 no-op）
- 子进程平面网络限制是 best-effort，裸 socket 可绕过

**影响**
- 无法提供真正的安全隔离
- 跨平台行为不一致
- 用户可能误以为自己处于安全沙箱中

**建议**
- 在文档中明确标注 `soft-limit` 不是安全边界
- 补完 Windows Job Object 支持
- 实现 `container` 或 `remote` 后端的真实隔离，或至少提供清晰 roadmap

---

### 4. Agent 核心职责仍过重

**问题**
`Agent` 同时管理：
- 运行循环
- 状态机
- steering / follow-up 队列
- 工具执行
- 上下文压缩触发
- 提醒注入
- 模式切换
- checkpoint 自动保存

**影响**
- 单元测试困难
- 新功能会继续膨胀核心
- 职责边界不清

**建议**
- 将 `StateMachine` 独立为子组件
- 将 `ReminderCoordinator` 独立
- 将 `CheckpointManager` 独立
- 将 `ModeSwitcher` 的运行时逻辑独立

---

### 5. 工具参数校验缺失统一机制

**问题**
内置工具（read/write/edit/bash 等）在 `Execute` 中手动检查参数：
- 缺少基于 JSON schema 的自动校验
- 缺少统一类型转换（如 `float64` → `int`）
- 错误信息不一致

**影响**
- 模型可能传入错误参数类型
- 每个工具都要重复写校验逻辑
- 容易漏检，导致运行时 panic 或错误行为

**建议**
- 基于 `models.ToolDefinition.Parameters` 自动生成校验器
- 提供统一的 `validateArgs(args, schema)` 工具函数
- 统一参数类型转换和错误提示

---

### 6. 会话分支功能不完整

**问题**
- `session.Session` 只支持线性历史
- JSONL 消息虽然保留 `parent_id` 字段，但 `Store` 没有提供分支导航能力
- fork/clone 功能在 TUI 中可能可见，但底层存储仍是线性

**影响**
- 产品功能与数据模型不一致
- 后续实现真正分支时可能需要重构存储层

**建议**
- 明确分支是产品目标还是已放弃
- 若是目标，需完整实现 `Session` 的树形操作
- 若放弃，应清理 `parent_id` 等遗留字段，避免误导

---

### 7. 工具执行错误处理不够统一

**问题**
- 部分工具返回 `(ToolExecutionResult, error)`
- 部分工具将错误信息放在 `result.Content` 中
- `Registry.Execute` 返回 `(result, isErr)`，但 `isErr` 只取决于 `Execute` 是否返回 error

**影响**
- 错误状态判断不一致
- TUI 和可观测性层展示错误时可能不一致

**建议**
- 统一工具错误模型
- `error` 表示工具执行失败（系统级）
- `ToolExecutionResult` 表示工具输出（业务级）
- 明确工具非零退出码是否应返回 error

---

### 8. LLM Provider 事件流与 Agent 事件流重复抽象

**问题**
- `pkg/llm/provider` 有自己的事件类型
- `pkg/llm/client.go` 转换为 `GatewayEvent`
- `pkg/events` 又有 `MessageStartEvent` / `MessageEndEvent` 等

**影响**
- 维护三层事件转换
- 容易在转换中丢失信息或语义偏差

**建议**
- 合并或简化事件模型
- 让 provider 直接产出上层需要的事件，减少中间层

---

### 9. 缺乏端到端 SWE 评估

**问题**
- 单元/集成测试覆盖良好，但缺少真实 SWE 任务端到端评估
- `eval/swe-bench-lite/` 已有基础目录和 runner，但似乎尚未跑通完整 pipeline

**影响**
- 无法衡量 Agent 在真实开发任务上的表现
- 难以判断改动是否真正提升能力

**建议**
- 完成 SWE-bench Lite MVP runner
- 跑 10-20 个任务建立 baseline
- 将结果和指标（resolve rate、cost、turns、duration）沉淀到 CI 或报告中

---

### 10. 事件总线可能成为主循环瓶颈

**问题**
事件总线用于：TUI 更新、session 持久化、observability、audit log、checkpoint。所有订阅者都在同一条同步总线上。

**影响**
- 一个慢订阅者（如 audit log 写盘）会阻塞主循环
- 事件处理失败难以隔离

**建议**
- 将事件订阅者按优先级分组
- 核心路径（session 保存）保持同步
- 非核心路径（observability、audit log）改为异步或带缓冲队列
- 提供事件处理失败的降级/丢弃策略

---

### 11. TUI 与核心循环耦合

**问题**
- `submitPromptCmd` 直接调用 `agent.Prompt`
- `agent.Prompt` 内部阻塞，在 goroutine 中运行
- 错误通过 `AgentDoneMsg` 返回
- 单元测试需要模拟 TUI 的异步 Cmd 模式

**影响**
- TUI 测试复杂
- Agent 核心难以单独测试

**建议**
- 提供同步的 `Runner.Run(prompt)` 接口
- TUI 只负责渲染事件，不直接驱动 Agent 内部循环

---

### 12. 配置验证不足

**问题**
- `config.Config` 加载后没有统一验证
- `sandbox.backend` 配成 `container` 会在运行时才报错
- `context.max_tokens` 等字段缺少非法值检查
- `permissions` 规则中的决策字符串不合法时也没有校验

**影响**
- 启动时报错晚
- 错误信息不清晰

**建议**
- 添加 `Config.Validate()` 方法
- 在启动时统一验证，尽早报错
- 为每个字段提供清晰的验证错误信息

---

### 13. 工具注册与发现机制不够灵活

**问题**
- 内置工具通过 `pkg/tools/builtin/init.go` 的 `init()` 注册
- HTTP 工具和 MCP 工具在 `prepareAgent` 中动态注册
- 扩展工具需要显式 import 包

**影响**
- 全局 `init()` 注册不够灵活
- 第三方扩展工具需要在主包中改动

**建议**
- 考虑使用插件目录或配置文件驱动工具注册
- 提供运行时工具加载能力
- 降低第三方扩展的接入成本

---

### 14. 缺少统一的测试夹具

**问题**
各包测试各自创建 fake 实现：
- `pkg/tools` 有 `fakeAwareTool`
- `pkg/agent` 有 `fakeAgent`
- `pkg/tui` 有 `fakeAgent`
- `pkg/sandbox` 有 `FakeSandbox`

**影响**
- 重复造轮子
- 测试辅助代码不一致
- 新测试难以复用已有夹具

**建议**
- 将通用测试工具集中到 `pkg/testutil` 或类似包
- 统一提供 fake LLM、fake sandbox、fake registry、fake session 等

---

### 15. 系统提示词与 Mode 的边界不清晰

**问题**
- `BuildSystemPrompt` 返回基础 prompt
- `Agent.applyMode` 会注入 mode-specific prompt
- 用户手动指定的 system prompt 与 mode prompt 的优先级关系不清晰

**影响**
- 用户可能困惑为什么某些 prompt 没有生效
- 模式切换时 prompt 行为可能不符合预期

**建议**
- 明确 system prompt 的覆盖优先级
- 在文档中说明 mode prompt 何时生效、如何覆盖

---

### 16. 上下文压缩对工具调用历史的影响待优化

**问题**
- 压缩后，旧的 tool_use/tool_result 对被 summary 替代
- 但模型在某些场景下需要看到完整的工具调用链
- 当前压缩策略不够智能

**建议**
- 评估保留关键工具调用历史的影响
- 提供配置让用户选择压缩策略（激进 / 保留工具历史）

---

## 三、优先级分类

### 🔴 高优先级（建议尽快处理）

1. **移除未使用的 Shannon 依赖** — 立竿见影减少依赖负担
2. **统一工具参数校验** — 提升健壮性，减少工具错误
3. **优化 checkpoint/session 存储策略** — 避免重复写盘和写放大
4. **完成 SWE-bench Lite MVP** — 建立真实能力 baseline
5. **补完配置验证** — 提升启动时报错的清晰度

### 🟡 中优先级

6. **进一步拆分 Agent 核心** — 长期维护性
7. **事件总线异步化/优先级分组** — 防止慢订阅者阻塞主循环
8. **统一测试夹具** — 提升测试质量
9. **TUI 与核心循环解耦** — 提升可测试性
10. **完善 sandbox 跨平台实现** — Windows 支持

### 🟢 低优先级 / 后续演进

11. 会话分支功能完整实现
12. 工具注册机制插件化
13. 智能压缩策略
14. 多版本 checkpoint
15. 系统提示词与 mode 优先级文档完善

---

## 四、总体评价

Lcoder 已经是一个**结构完整、可运行、可扩展**的 code agent 系统：

- 工具系统（内置 + HTTP + MCP）可工作
- 上下文管理（blocks、预算、cache breakpoints、压缩）较完整
- 沙箱抽象已落地，默认 passthrough 保证零回归
- Checkpoint 机制已接入启动和运行流程
- TUI 可用，交互功能基本齐全
- 测试覆盖良好，核心包测试通过

主要问题是**收尾和去杂**：
- 旧文档未更新，可能误导
- 无用重型依赖未清理
- 一些设计已落地但缺少用户可见的文档
- 真实 SWE 评估缺失，无法验证实际能力
- 核心代码仍较臃肿，需要进一步拆分

整体架构健康，**不需要推倒重来**。建议优先做一轮“清理依赖 + 统一校验 + 补齐评估 + 文档更新”的工作，为后续功能演进打下更稳固的基础。

---

*报告由代码扫描与实现分析生成，后续可针对任一问题展开具体实施方案。*
