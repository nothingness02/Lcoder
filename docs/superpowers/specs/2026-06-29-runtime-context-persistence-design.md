# 运行时上下文持久化 + 压缩一次性提交 设计

日期:2026-06-29
状态:已批准,待实现计划

## 1. 背景与问题

ctxmgr(`pkg/contextmgr`)需要维护一份"运行时上下文",并把它作为一个状态:TUI 关闭后保存、重启后加载,每个对话只对应一个状态。压缩触发前的对话记录要实时落盘,直到最终压缩触发后重置该 session 的磁盘记录,以平衡**成本、真实性、用户体验**。

当前实现存在三个具体问题:

1. **session 是全量真相、无限增长**:`Session.AppendMissing` 把 agent 窗口里的全部消息按 ID 镜像进 JSONL,每条消息触发一次整文件 `Save()`。磁盘上永远保存未压缩的完整历史。
2. **压缩是瞬时的、每轮重算**:生产路径走 `streamAssistant` 的 `else` 分支 → `Manager.BuildTurnRequest` → `KeepRecentInBudget.Apply` → `fitWithCompaction` → `compactRecent` → `mgr.summarizer`。返回的是新的 block 切片,**从不回写 `m.blocks`**。结果:只要 token 超过 `CompactLimit`,**每一轮都重新调用 LLM summarizer 重新总结同一批老消息**——成本高、且每轮总结非确定性。
3. **重启恢复的是原始消息而非运行态**:`prepareAgent` 读 `sess.ActiveMessages()`(全量分支)塞进 recent 块,恢复的是原始记录,不是用户最后看到的压缩后运行态。

`makeTransformContext`(`cmd/lcoder/wiring.go`)是另一条 opt-in 的 `TransformContext` 压缩路径,生产 `prepareAgent` 未接入(扩展可用),本设计不改动它。

## 2. 已确认的决策

1. **压缩后原始老消息:直接丢弃**(成本优先)。session 磁盘文件重置为"摘要 + 近期尾巴"。
2. **重启恢复形态:恢复用户最后看到的运行态**(压缩态),不再重新总结,启动即用,成本最低。
3. **压缩改为一次性提交**:压缩触发时把摘要回写进 manager 的 recent 块并持久化,同一批老消息只总结一次;并**滚动折叠**(再次超阈值时把旧摘要+这期间新消息折叠成新摘要),保证摘要恒为一条、长期有界。
4. **删除 session tree**:全删 fork + clone + 会话内 `ParentID`/`buildBranch`/`ActiveBranch` 整套机制。session 退化为纯线性消息列表。
5. **实现路径 A**:manager 主导提交 + 事件驱动重写 session。

## 3. 核心模型

**运行时上下文状态 = manager 的 recent(对话)块 = session JSONL 本身。**

- `system` / `project_docs` / `skills` 三个块在启动时现重建(`BuildSystemPrompt`、context loader、skills loader),**不持久化**。
- 唯一需要持久化的是对话块,而它正是 session JSONL 存的内容。**不引入第二份状态文件**(避免两份真相);压缩态(摘要+尾巴)作为普通消息写进同一个 JSONL,摘要是一条带 `compacted:true` 元数据的 system 消息。
- 这正好对应"每个对话只对应一个状态"与"重置 session 对应的对话记录"。

## 4. 详细设计

### 4.1 session 线性化(`pkg/session/store.go`)

- `Session.Messages []models.AgentMessage` 改为纯线性列表(提交顺序)。
- 删除 `ActiveBranch` 字段、`Append` 中的 `ParentID` 标记、`buildBranch`、`ActiveMessages`。
  - `models.AgentMessage.ParentID` 字段本身保留(属 models 包,移除超出范围),session 不再使用它。
- `Append(msg)`:打元数据(session_id / cwd / saved_at)后 `s.Messages = append(...)` + `Save()`。
- `Load(path)`:按文件顺序读入 `Messages`,去掉 buildBranch 调用。
- **新增 `Replace(msgs []models.AgentMessage) error`**:`s.Messages = msgs`,整文件原子重写(临时文件 + rename)。
- 调用方 `cmd/lcoder/main.go` 中 `sess.ActiveMessages()` 改为 `sess.Messages`。

### 4.2 删除 tree(全删)

- 删除 `pkg/session/tree.go`(`Fork`/`Clone`/`Tree`)与 `pkg/session/tree_test.go`。
- 更新 `pkg/session/store_test.go`、`pkg/session/append_missing_test.go`(去掉 ActiveBranch 相关断言)。
- `cmd/lcoder/main.go`:移除 `root.AddCommand(forkCmd())` / `cloneCmd()`。
- `cmd/lcoder/commands.go`:删除 `forkCmd`、`cloneCmd` 函数。
- TUI:
  - `pkg/tui/keys.go`:移除 `case "sessions", "fork"` 中的 `"fork"` 处理。
  - `pkg/tui/menu.go`:移除 `{Name: "fork", ...}` 菜单项。
  - `pkg/tui/sessionpicker.go`:移除 fork 模式、`ForkCurrent`、`SessionStore` 接口中的 `Fork`。
  - `pkg/tui/model_test.go`:移除 `fakeSessionStore.Fork`。

### 4.3 压缩一次性提交(`pkg/contextmgr/manager.go`)

新增方法:

```go
// MaybeCompact 在总 token 超过 CompactLimit 且配置了 summarizer 时,把 recent 块
// 较早的消息折叠为一条摘要并原地回写;否则不动。返回是否实际提交了压缩。
func (m *Manager) MaybeCompact() (committed bool, err error)
```

行为:

1. 总 token ≤ `CompactLimit()` 或 `summarizer == nil` → 返回 `false, nil`。
2. 取 recent 块,切分 `older / tail`:
   - 保留至少 `MinRecent` 条;
   - 保证最后一条 `user` 消息落在 `tail` 内;
   - 若 recent 头部已是 `compacted:true` 摘要,则把它并入 `older` 一起重新总结(**滚动折叠**)。
3. `tail` 头部 strip 掉孤儿 `tool_result`(复用 `stripLeadingOrphanToolResults` 逻辑)。
4. `summarizer(older)` 生成新摘要;构造一条 `RoleSystem` + `compacted:true` 的摘要消息。
5. 原地 `ReplaceRecent([summary] + tail)`,返回 `true, nil`。
6. summarizer 失败(已被 `compaction.CircuitBreaker` 包裹)→ 返回 `false, err`;调用方视为非致命。

`MinRecent` 通过 Manager 字段 + `WithMinRecent(n)` Option 传入,来源 `cfg.Context.MinRecent`(`agentsetup.NewContextManager` 设置)。

`compactRecent` 中复用的切分逻辑提取为 manager 内的共享 helper。

### 4.4 window policy 退化为截断兜底(`pkg/contextmgr/window.go`)

- 删除基于 summarizer 的瞬时压缩路径:`fitWithCompaction`、window 内的 `compactRecent`、`Apply` 里的 eager-compaction 分支。
- `KeepRecentInBudget.Apply` 只保留 `fitWithoutCompaction`(截断/丢弃 dynamic 块尾巴 + `ensureLastUser`),作为安全网:即便压缩被跳过或失败,也绝不超过 `MaxTotal`。
- policy 不再依赖 `mgr.summarizer`。

### 4.5 触发时机 + 事件(`pkg/agent/loop.go`, `pkg/events`)

- `pkg/events`:新增事件类型 `CompactionCommitted`,仅作信号(落盘 handler 自行读 `agent.AllMessages()` 取压缩后窗口)。
- `Agent.run`:每轮在 `streamAssistant` 之前调用 `a.mgr.MaybeCompact()`:
  - `err != nil` → 记录/发 Error 事件但不中断本轮(非致命降级)。
  - `committed == true` → `a.bus.Emit(CompactionCommitted{...})`。
- 在 TUI / one-shot / JSON 三种模式下统一生效(都跑同一个 agent loop)。

### 4.6 落盘(事件驱动,单一处理点)

bus 订阅的 persist handler(`cmd/lcoder/main.go` 的 `persistHandler`,`runTUI` 与 `runOneShot` 共用)处理:

- 普通轮结束(`MessageEndEvent` / `ToolExecutionEndEvent` / `AgentEndEvent`)→ `sess.AppendMissing(agent.AllMessages())`(压缩前实时追加,全保真)。
- `CompactionCommitted` → `sess.Replace(agent.AllMessages())`,JSONL 重写为压缩态、丢弃老消息。

> 注:统一以 bus handler 作为唯一落盘点,避免 TUI `persistSession` 与 bus handler 双重处理压缩重写。TUI `onAgentDone` 仍可调 `persistSession`(走 `AppendMissing`,幂等)。

### 4.7 TUI 体验(`pkg/tui`)

- **实时 UI 不塌缩**:压缩只影响"发给模型的上下文 + 落盘状态"。TUI 实时滚动区保持完整历史可读,收到 `CompactionCommitted` 时仅追加一行系统提示:`↧ 已压缩早前对话以节省 token(原始记录已合并为摘要)`。
- **重启后**从压缩态 JSONL 重建显示(`NewModel` 已用 `ag.AllMessages()` 重建 `m.blocks`),即"恢复用户最后看到的运行态"。

## 5. 边界与降级

1. **AutoCompact 关闭** → `summarizer == nil` → `MaybeCompact` no-op,走截断兜底;session 随会话增长(与现状一致,属 opt-in)。
2. **summarizer 失败** → 熔断器返回错误,`MaybeCompact` 返回 `committed=false`;不重写 session,本轮以截断兜底继续。与现有降级一致。
3. **压缩后 tail 仍超预算**(单条超大消息)→ `BuildTurnRequest` 截断兜底确保不超 `MaxTotal`。
4. **重写中崩溃** → `Replace` 采用临时文件 + rename 原子写,避免 JSONL 损坏。崩溃发生在 manager 已 mutate 但 rename 前时,下次启动加载旧(未压缩)JSONL,下一轮重新压缩,安全收敛。
5. **fork/clone 删除后果** → 不再支持从历史点分叉或复制会话;符合"全删"决策。

## 6. 涉及文件清单

- `pkg/session/store.go` — 线性化、`Replace`、简化 `Append`/`Load`。
- `pkg/session/tree.go` — 删除。
- `pkg/session/tree_test.go` — 删除;`store_test.go`、`append_missing_test.go` — 更新。
- `pkg/contextmgr/manager.go` — `MaybeCompact`、`MinRecent` 字段/Option、切分 helper。
- `pkg/contextmgr/window.go` — 删除 summarizer 压缩路径,仅留截断兜底。
- `pkg/agent/loop.go` — 每轮调用 `MaybeCompact` 并发事件。
- `pkg/events/*` — 新增 `CompactionCommitted`。
- `cmd/lcoder/main.go` — 移除 fork/clone 注册;persistHandler 处理 `CompactionCommitted` → `Replace`;`ActiveMessages` → `Messages`。
- `cmd/lcoder/commands.go` — 删除 `forkCmd`、`cloneCmd`。
- `pkg/tui/{keys.go,menu.go,sessionpicker.go,model.go,model_test.go}` — 删除 fork;新增压缩提示行。

## 7. 测试策略

- `pkg/session`:`Replace` 重写并能正确 reload;`Append`/`Load` 线性顺序;原子写不破坏旧文件。
- `pkg/contextmgr`:`MaybeCompact` 在超阈值时折叠并原地回写(摘要恒一条、滚动折叠);未超阈值/无 summarizer 时 no-op;summarizer 失败时非致命返回 `false`。
- `pkg/contextmgr/window`:`Apply` 不再调用 summarizer,仅截断兜底,且不超 `MaxTotal`。
- `pkg/agent`:每轮触发 `MaybeCompact`;提交时发 `CompactionCommitted`。
- 集成:压缩提交后 session JSONL 被重写为压缩态;重启 reload 恢复压缩态;压缩前 `AppendMissing` 实时落盘。
- 删除验证:fork/clone 命令与 TUI 入口移除后编译通过、相关测试删除/更新。
