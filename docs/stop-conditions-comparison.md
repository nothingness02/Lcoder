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
| `ShouldContinueAfterStop` | ✅ 新增 | ✅ 核心机制 | ❌ (followUp 替代) | ❌ |
| `FollowUp` 队列 | ✅ (无消费者) | ❌ | ✅ | ❌ |
| steering 中途注入 | ✅ | ✅ `flushSteerBuffer` | ✅ | ❌ |
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

### Lcoder 独有：FollowUp 队列

设计了但无消费者。Kimi Code 的 `shouldContinueAfterStop` 在功能上等价，但 Kimi Code 的 hook 可以调 LLM、等异步、检查 goal 预算，比 followUp 强大得多。

### Kocoro 独有：看门狗

生产级 daemon 场景需要的 idle 超时检测——如果 agent 在某个阶段停滞超过 N 秒，自动取消。Lcoder 的 TUI 模式不需要（用户会按 Ctrl+C），但如果未来支持 daemon 模式，这是必须的。

## 建议

| 优先级 | 改进 |
|:---:|------|
| ★★★ | 添加 `max_turns_per_run` 配置 + 硬上限检查 |
| ★★ | 添加 `StopReason` 类型（`tool_use` / `end_turn`），传给 `ShouldContinueAfterStop` |
| ★ | provider `truncated` 检测（低优先级，`ResolveMaxTokens` 已做防护） |
| — | FollowUp 保留，等有编排器时再接入 |
