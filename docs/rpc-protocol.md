# Lcoder RPC 协议（stdio JSONL）

`lcoder rpc` 以 JSONL RPC 模式运行 agent：命令走 stdin、响应/事件/审批请求走 stdout，一行一个 JSON 对象。任何语言实现的 UI 都可以通过管道驱动 agent，无需链接 Go 代码。

实现位于 `pkg/rpcserver`（只依赖 `pkg/agentapi` + `pkg/events`，不依赖 `pkg/tui`）；`cmd/lcoder` 负责组装（与 TUI 相同的 `prepareAgent` + `host.NewCore` 流程，`--session`/`--continue`/`--mode`/`--model` 等既有 flag 同样可用）。

```bash
lcoder rpc --provider openai --model gpt-4o-mini
```

## Framing

- 严格的 JSONL：记录之间只以 `\n` 分隔（会兼容剥离行尾 `\r`）。单行上限 16 MiB——**超限不是跳行而是杀连接**：scanner 报错后 `Serve` 返回错误、进程按错误路径退出，客户端应把它当致命协议错误处理。
- `encoding/json` 不产生内嵌换行，每条记录恰好一行。
- **不要**用把 `U+2028`/`U+2029` 当换行的通用行读取器（如 Node `readline`）——它们是 JSON 字符串内的合法字符。
- rpc 模式下 stdout 只由 rpcserver 写入；日志、警告、observability 一律走 stderr 或文件。

## 信封

所有字段名均为 snake_case。

命令（stdin）：

```json
{"id": "cli-1", "type": "prompt", "text": "..."}
```

`id` 可选：带 `id` 的命令必有一条对应响应；不带 `id` 是 fire-and-forget（照常执行，不产生响应）。

响应（stdout）：

```json
{"type": "response", "id": "cli-1", "ok": true, "data": {...}}
{"type": "response", "id": "cli-1", "ok": false, "error": "..."}
```

`ok: true` 只表示命令被受理/执行成功；`prompt`/`continue` 受理后运行完成是经 `agent_end` 事件通知的，受理后的失败以 `error` 事件形式出现（与 pi 的语义一致）。**受理响应保证先于本 run 的任何事件到达客户端**——客户端看到 `{ok:true}` 后即可按事件流驱动 UI。

边界情况：若 run 在 `agent_start` 之前失败（典型：会话落盘失败，`Core.Prompt` 同步报错），线上不会出现孤立的 `error` 事件——服务端在 `error` 事件后补发一个合成的 `agent_end`（`reason: "error"`，无对应 `agent_start`），保证受理的 run 总有 start/end 配对。

事件（stdout，异步流式）：

```json
{"type": "event", "event": {"type": "agent_start", "turn": 1}}
```

`event` 字段就是 `pkg/events` 的 JSON 形态（18 种事件，`events.UnmarshalJSON` 可按 `type` 判别反序列化）。事件不携带命令 `id`。

协议级错误（畸形 JSON、未知命令类型、缺 `type`）**总是**返回错误响应（能解析出 `id` 就带上），以便开发期暴露客户端 bug。

## 命令表

无返回值的命令响应无 `data` 字段；payload 未列出的命令无参数。

### 运行控制

| 命令 | payload | 说明 |
|---|---|---|
| `prompt` | `{text}` | 受理即响应 `{ok:true}`（响应先于本 run 的所有事件），运行在后台，完成经 `agent_end` 事件通知。运行中再发 → `{ok:false, error:"agent is running"}`；goal 追求中再发 → `{ok:false, error:"a goal pursuit is active; ..."}`（改用 `steer`） |
| `continue` | — | 不带新用户消息启动一轮，busy 规则同 `prompt` |
| `steer` | `{text}` | 运行中注入用户消息，在下一个安全边界生效 |
| `abort` | — | 随时可用；优雅停止当前 run；挂起的审批以取消语义解除 |

### Busy 规则（状态变更命令）

以下命令在任何 run 在飞（ad-hoc run 或 goal 追求，含追求的 turn 间隙）时**快速失败** `{ok:false, error:"agent is running"}`，不落到 host 兜底：

`set_mode` / `open_session` / `new_session` / `truncate_after` / `restore_checkpoint` / `goal_start` / `goal_resume`

host 侧同规则返回的 `host.ErrAgentBusy` / `host.ErrCoreClosed` 也会被映射为稳定的 wire 文案 `"agent is running"` / `"core is closed"`（不透传内部细节）；其他错误维持原文透传。

### 模式 / 模型 / 思考

| 命令 | payload | 响应 data |
|---|---|---|
| `set_mode` | `{mode}` | — |
| `set_model` | `{provider, model_id}` | `{"model": {"provider", "id"}}` |
| `set_thinking` | `{value}` | —；`""`=不发送、`"off"`、`"on"` 或模型 effort 级别 |
| `clear_skill_filter` | — | — |

注意 `set_model` 的模型字段是 `model_id` 而非 `id`——信封的 `id` 保留给请求关联。TokenBudget 由服务端按 TUI provider 面板同一套逻辑推导（catalog 窗口/maxOutput + `config.ResolveContextBudget` + `Context.StaticRatio`），客户端无需关心。

### 会话

| 命令 | payload | 响应 data |
|---|---|---|
| `new_session` | — | `{"session_id"}` |
| `open_session` | `{session_id}` | `{"session_id"}` |
| `list_sessions` | — | `{"sessions": [SessionInfo...]}`（含 subagent 标记） |
| `rename_session` | `{session_id, title}` | — |
| `truncate_after` | `{message_id}` | —；pi 式 fork 语义（`/retry`），空串从根分叉 |

### Goal 追求

| 命令 | payload | 说明 |
|---|---|---|
| `goal_start` | `{objective, turn_budget, token_budget}` | 预算 0 = 不限；追求循环在 host 侧异步驱动，进展经 `goal_updated` 事件流出 |
| `goal_pause` | `{reason}` | 在当前 run 边界暂停 |
| `goal_resume` | — | 恢复 paused/blocked 的 goal |
| `goal_cancel` | — | 清除 goal 记录 |

goal 追求进行中 `prompt`/`continue` 会被拒绝（会撞 driver 自己的 run），与 agent 对话请用 `steer`；追求进行中 `goal_start`/`goal_resume` 同样按 busy 规则拒绝（换目标请先 `goal_cancel` 再 `goal_start`）。

### Checkpoint

| 命令 | payload | 响应 data |
|---|---|---|
| `save_checkpoint` | — | `{"checkpoint_id"}` |
| `restore_checkpoint` | `{checkpoint_id}` | — |
| `list_checkpoints` | — | `{"checkpoints": [{"id"}...]}` |

### 状态快照

`get_state`（无 payload）返回完整引导快照，新接入的客户端用它重建 transcript 与状态栏：

```json
{
  "type": "response",
  "id": "...",
  "ok": true,
  "data": {
    "session_id": "...",
    "mode": "code",
    "thinking": "on",
    "model": {"provider": "openai", "id": "gpt-4o-mini"},
    "running": false,
    "goal": {"objective": "...", "status": "active", "turn_budget": 0,
             "token_budget": 0, "turns_used": 2, "tokens_used": 1530,
             "block_reason": "..."} ,
    "tasks": [{"text": "...", "status": "pending"}],
    "context_stats": {"total": 42, "budget_max": 128000, "...": "..."},
    "capabilities": ["tools"],
    "messages": [{"id": "...", "role": "user", "content": [{"type": "text", "text": "..."}], "...": "..."}]
  }
}
```

- `goal` 为 `null` 表示无 goal 记录。
- `running` 来自 host 的 `Running()`：任何 run 在飞都为 `true`——ad-hoc `prompt`/`continue`，或 goal 追求（driver 在 turn 间隙也持有运行槽，追求全程为 `true`）。`agent_end` 到达后运行 goroutine 收尾前可能有极短窗口仍为 `true`（busy 标志只由 run goroutine 自己清除），客户端续跑应以收到 `{ok:true}` 为准而非轮询 `running`。
- `messages` 是全量 `AllMessages`——**事件没有 journal/replay 机制，`get_state` 是补历史的唯一手段**（v1 限制）。
- `capabilities` 来自启动时配置模型的 catalog 声明，未知时省略。

## 审批往返（反向请求）

权限引擎判定需要人工确认时，服务端发出（方向：stdout）：

```json
{"type": "approval_request", "id": "srv-1",
 "request": {"tool_call_id": "b1", "tool_name": "bash",
             "args": {"command": "rm -rf /tmp/x"},
             "command": "rm -rf /tmp/x"}}
```

`command` 仅 bash 调用存在（便捷字段）。`request` 是 `agentapi.ToolCallInfo` 的 snake_case 投影（不含完整上下文消息——渲染确认弹窗只需这些）。

客户端回答（stdin）：

```json
{"type": "approval_response", "id": "srv-1", "result": {"scope": "once"}}
```

`scope` 取值：`deny` / `once` / `session` / `project` / `global`（映射 `agentapi.ConfirmScope`；未识别的值按 `deny` 处理——畸形回答绝不放大权限）。`approval_response` 不产生 `response` 信封：它的 `id` 属于服务端发出的请求，再回响应会污染客户端的关联表。未知/过期 id 静默丢弃。

服务端在收到回答前一直阻塞——**审批没有超时**：客户端不回答，run 就一直挂着（现状，有意为止；超时策略留待后续版本）。`abort`（run ctx 取消）或客户端断开（stdin EOF）会解除所有挂起审批，分别以取消/断开错误终止等待，agent run 随之结束。

## 生命周期

- stdin EOF → 优雅停止 agent（`Abort` + 等待 run 收尾）、写 best-effort checkpoint、退出码 0。
- SIGINT/SIGTERM → 沿用 main.go 的 crash checkpoint 机制。

## v1 限制

- 事件无 journal/replay：断线重连后只能靠 `get_state` 的 `messages` 全量补历史。
- 单客户端：协议无 session/agent 路由字段（与 `CoreAPI` 的单会话约定一致）；多 session 与 HTTP/SSE 传输留待后续阶段。
- `prompt`/`steer` 只支持文本（无图片附件）。
