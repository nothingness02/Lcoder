# Compaction 与 Session 持久化重设计

日期:2026-07-13
状态:已批准,待写实现计划

## 背景

对照 pi 的 compaction / branch summarization 文档分析 Lcoder 当前实现
(`pkg/contextmgr`、`pkg/compaction`、`pkg/session`、`pkg/agent/loop.go`),
发现五类边界条件问题。本设计覆盖其中的正确性问题与持久化改造
(用户已确认范围:A/B/C/D + E;不做分支摘要、手动 /compact、事件载荷、文件跟踪)。

## 问题清单

| # | 问题 | 边界后果 |
|---|------|---------|
| A | 保留策略按消息条数(`keepRecent=10`)不按 token | 10 条大 tool_result 仍可能占 100k+ token,压缩后照样溢出;短消息则被过度压缩 |
| B | 单轮超大(split turn)无法处理 | 最后一轮自身超保留预算时,`lastUser` 保护使整轮保留,窗口仍溢出,window policy 静默截断丢消息且无摘要 |
| C | 摘要请求本身可能溢出 | summarizer 收到原始消息,巨大 tool_result 整体传入导致摘要调用自身超窗失败;熔断 3 次后永久跳过压缩,退化为静默截断 |
| D | 摘要不可取消 | summarizer 用 `context.Background()`,abort/Ctrl+C 无法中断最长 90s 的摘要调用,与 "Abort cancels the whole run context" 相悖 |
| E | `Session.Replace` 破坏性持久化 | 压缩提交时磁盘原始消息被整体丢弃:无法 rewind 到压缩前状态;其他分支消息全部丢失;eval/审计拿不到完整轨迹 |

## 总体方案

token 预算切点 + split-turn 双摘要 + 可取消摘要管线 + 文件内 append-only
CompactionEntry。改动边界:`pkg/contextmgr`(fold 逻辑)、`pkg/compaction`
(摘要管线)、`pkg/session`(条目与视图重建)、`pkg/config` / `pkg/agentsetup`
(配置)。不动 window policy 与 checkpoint 整体结构。

## 第 1 节:切点与保留策略(A + B)

### token 预算替代条数

- `Manager` 新增 `keepRecentTokens int`,默认 20000(对齐 pi
  `keepRecentTokens`),`WithKeepRecentTokens(n)` 注入。
- `keepForLevel` 返回 token 预算:
  - proactive:`keepRecentTokens`
  - preflight:`keepRecentTokens / 2`
  - reactive:`keepRecentTokens / 5`(约 4k,只够保住最关键尾部,允许切进当前轮)
- 现有的 `keepRecent`(条数)降级为下限保护:切点不得使保留尾部少于
  `min(keepRecent, 总消息数)` 条且必须包含最后一条 user 消息——短会话不因
  token 预算而过度压缩。两个约束都满足时取保留更多的切点。

### 切点搜索算法

从尾部向前累积 `estimator` 估算 token,直到达到预算,得到候选切点 i,
然后按规则调整:

1. **合法切点**:只允许切在 user 或 assistant 消息之前。若 i 落在
   tool_result 消息上,向前回退到其配对的 assistant tool_use 之前
   (tool_use/tool_result 必须整体保留或整体折叠,等价于 pi 的
   "never cut at tool results")。
2. **最后 user 保护**:切点不得晚于最后一条 user 消息的位置(保证尾部
   含完整最新用户意图)。

### split turn

若按预算的切点晚于(或等于)最后一个 turn 的起点,即最后一轮自身超预算:

- 允许切在该 turn 内部的 assistant 消息处(tool_use/tool_result 配对仍不拆)。
- 生成两段摘要:
  1. history 摘要:turn 起点之前的消息(若此前已有压缩摘要,它作为普通消息
     在此段内被重新总结);
  2. turn prefix 摘要:turn 起点到切点的部分。
- 合并为一条 summary 消息:

  ```
  [Summary of earlier conversation]
  <history 摘要>

  [Summary of current turn so far]
  <turn prefix 摘要>
  ```

  仍带 `metadata.compacted=true`。history 段为空时省略第一段标题。

### 兜底

若 split-turn 折叠后尾部仍超 `DropLimit`(单条 assistant 消息本身就超大),
维持现有 window policy 截断兜底,但在 TUI/日志显式提示发生了截断
(借现有 Error 事件通道),不再静默。

## 第 2 节:摘要管线(C + D)

### 签名变化

```go
// pkg/contextmgr 与 pkg/compaction 同步修改
type SummarizeFunc func(ctx context.Context, messages []models.AgentMessage) (string, error)
```

- `foldOlder` 接收并透传 agent 的 run context;`NewLLMSummarizer` 用传入的
  ctx 派生 timeout 子 context,替换 `context.Background()`。abort/Ctrl+C
  立即取消摘要,`foldOlder` 返回 `ctx.Err()`,runtime 状态不变(摘要成功
  前不替换 recent 块,天然原子)。
- 受影响面(编译期可见,逐一修复):`agentsetup`、`SimpleSummarize`、
  `cmd/cache-eval`、各包测试与 integration 测试。

### 序列化与截断

摘要输入从"原始消息数组"改为纯文本序列化(新文件
`pkg/compaction/serialize.go`):

```
[User]: <text>
[Assistant]: <text>
[Assistant tool calls]: read(path="foo.ts"); edit(path="bar.ts", ...)
[Tool result]: <text,单条截断 2000 字符,超出替换为 ...[truncated N chars]>
```

- 作为单条 user 消息发给摘要模型,配合现有双阶段 system prompt
  (`<analysis>`/`<summary>` 不变),防止模型把输入当作待继续的对话。
- 序列化后若总估算 token 仍超摘要模型窗口的 50%,按 `[Tool result]`、
  `[Assistant]`、`[User]` 的优先级顺序进一步截断,保证摘要请求自身不溢出。
- `keepRecentTokens` 上限为有效输入窗口的 30%(防止保留尾部过大导致下一轮
  又立刻触发压缩)。

### 熔断语义修正

熔断器 OPEN 时只跳过 LLM 摘要调用,`MaybeCompactLeveled` 回退到
`foldOlderWithoutSummary`:不写摘要,只把 older 段丢弃并保留合法尾部,
同时发 Error 事件告知"压缩降级为截断"。杜绝"熔断 → 永久不压缩 → 窗口
静默截断"的最差路径。

## 第 3 节:持久化与视图重建(E)

### CompactionEntry(文件内,append-only)

压缩提交时向 session JSONL **追加**一条特殊消息,原始消息一律不动:

```json
{
  "id": "cmp-<uuid12>",
  "role": "system",
  "parent_id": "<当时的 branch head>",
  "content": [{"type": "text", "text": "<summary 全文>"}],
  "metadata": {
    "type": "compaction",
    "first_kept_entry_id": "<kept 尾部第一条消息的 id>",
    "tokens_before": 123456,
    "read_files": [],
    "modified_files": []
  }
}
```

`read_files`/`modified_files` 字段预留,本次不填(文件跟踪不在范围内)。

### Session API

- 新增 `Session.AppendCompactionEntry(summary, firstKeptEntryID string, tokensBefore int) error`:
  以当前 branch head 为 parent 追加条目;**不 rewrite 文件**,在现有
  `Save()` 的 tmp+rename 原子写基础上改为单条 append(对已有文件直接
  append 一行;文件不存在时走 Save),保留原子性语义。
- 新增 `Session.EffectiveMessages() []models.AgentMessage`:
  1. 沿 active branch 的 parent_id 链从 head 向前找最新 CompactionEntry;
  2. 找到:视图 = 摘要消息(`role=system`,`metadata.compacted=true`) +
     链上 `first_kept_entry_id` 起的后续消息;`first_kept_entry_id` 不在
     链上时(悬挂 id)回退到 CompactionEntry 自身之后的消息;
  3. 未找到:同现有 `ActiveMessages()`。
  链上可能存在多条 CompactionEntry(多次压缩),只用最新一条——更老的
  entry 对应的原始消息仍在磁盘,满足审计/回溯。
- `main.go` 两处持久化订阅:`CompactionCommittedEvent` 分支从
  `sess.Replace(ag.AllMessages())` 改为
  `sess.AppendCompactionEntry(summary, firstKeptEntryID, tokensBefore)`。
  摘要文本、firstKeptEntryID、tokensBefore 需要随事件传递——为此
  `CompactionCommittedEvent` 增加三个字段(这是事件载荷的最小必要扩展,
  不做额外观测字段)。
- 旧 `Replace` 方法保留给测试使用,标注 deprecated。

### 启动加载

`prepareAgent` 的 `sess.ActiveMessages()` 改为 `sess.EffectiveMessages()`,
`contextmgr.Manager.SetMessages` 逻辑不变(compacted summary 进 recent 块)。

### 边界

- **向后兼容**:无 CompactionEntry 的旧 session 走 ActiveMessages,行为不变;
  已被旧 Replace 压缩过的 session(含 `metadata.compacted` 摘要消息)按
  线性历史加载,摘要自然保留在消息流里,行为不变。
- **分支**:非 active 分支消息在 JSONL 中不动;后续在压缩过的分支上 Fork,
  parent_id 链仍可达 CompactionEntry,视图重建一致。
- **checkpoint**:checkpoint 不存消息(从 session 加载),唯一需要确认的是
  checkpoint 记录的 block 元数据与 EffectiveMessages 重建出的 recent 块一致;
  若 checkpoint 创建于压缩前、恢复于压缩后,recent 块以 session 视图为准
  (现状逻辑),不新增字段。

## 第 4 节:配置对齐(test / 生产 / eval)

- `pkg/config/config.go`:`ContextConfig` 新增
  `keep_recent_tokens int`(`yaml:"keep_recent_tokens"`,0 = 默认 20000);
  `Validate` 校验非负;加入 env override 映射。
- `pkg/agentsetup/setup.go`:`WithKeepRecentTokens(cfg.Context.KeepRecentTokens)`。
- `configs/lcoder.yaml`:补 `keep_recent_tokens: 20000` 及注释。
- eval 对齐:`eval/swe-bench-lite` 与 `cmd/cache-eval` 的 context 配置显式
  设置相同值,保证可复现,不依赖默认值漂移。
- test 对齐:现有按条数断言的 compaction 测试改为按 token 构造消息。

## 第 5 节:测试策略

| 边界 | 测试 |
|------|------|
| A token 切点 | 大小混合消息,断言保留尾部 token ≤ 预算且切点在 user/assistant 边界 |
| 切点不拆对 | 预算恰好落在 tool_result 后,断言 tool_use/tool_result 配对完整保留 |
| B split turn | 单轮超预算,断言两段摘要被调用、turn 尾部保留、合并格式正确 |
| C 序列化截断 | 超大 tool result,断言摘要输入含截断标记且总长有界 |
| D 取消 | 摘要中 cancel ctx,断言 foldOlder 快速返回且状态未变 |
| 熔断降级 | 熔断 OPEN 时断言走截断路径且发 Error 事件 |
| E1 追加不丢数据 | 压缩后 JSONL 行数 = 原消息数 + 1,原始消息逐条仍在 |
| E2 视图重建 | 含 CompactionEntry 的 session 加载后 EffectiveMessages = 摘要+kept;多次压缩链正确 |
| E3 悬挂 id | 构造失效 firstKeptEntryId,断言回退到 entry 自身之后 |
| E4 分支共存 | 分支 session 压缩后另一分支消息仍在 |
| E5 向后兼容 | 旧格式(无 entry)与旧 Replace 格式 session 加载正常 |
| 集成 | `test/integration` 新增"压缩→崩溃→重启→视图恢复"round-trip(llmtest 客户端,无需 API key) |
| 回归 | `go test $(go list ./... \| grep -v 'reference/Shannon')` 与 `-race` 全绿 |

## 明确不做(YAGNI)

- 分支摘要(branch summarization)、`/tree` 导航
- 手动 `/compact` 命令
- 文件操作累计跟踪(read_files/modified_files 实际填充)
- CompactionEntry 从磁盘隐藏原始消息的"垃圾回收"
- window policy 与 checkpoint 结构改动
