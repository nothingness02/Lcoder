# 运行时上下文持久化 + 压缩一次性提交 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 ctxmgr 维护的运行时上下文(recent 块)成为单一持久化状态:压缩前实时落盘、压缩一次性提交并把 session JSONL 重置为压缩态、重启恢复压缩态;同时删除 session tree(fork/clone/分支)改为纯线性模型。

**Architecture:** 运行时状态 = manager 的 recent 块 = session JSONL。压缩由 `Manager.MaybeCompact` 在每轮前一次性提交并原地折叠(滚动),agent 发 `CompactionCommitted` 事件,持久化层据此 `Session.Replace` 重写 JSONL。window policy 退化为截断兜底。session 去掉 ParentID/buildBranch/ActiveBranch。

**Tech Stack:** Go;包 `pkg/session`、`pkg/contextmgr`、`pkg/agent`、`pkg/events`、`pkg/tui`、`cmd/lcoder`。测试用标准 `go test`。

参考 spec:`docs/superpowers/specs/2026-06-29-runtime-context-persistence-design.md`

---

## 文件结构

- `pkg/session/store.go` — 线性会话模型 + `Replace`(原子写)。
- `pkg/session/tree.go` — 删除(Fork/Clone/Tree)。
- `pkg/session/{store_test.go,tree_test.go,replace_test.go}` — 测试增删改。
- `pkg/contextmgr/manager.go` — `MaybeCompact`、`WithMinRecent`、`SetMessages` 摘要健壮性。
- `pkg/contextmgr/window.go` — 删除 summarizer 压缩路径,仅留截断兜底。
- `pkg/contextmgr/{manager_compact_test.go,window_test.go}` — 测试。
- `pkg/events/types.go` — `CompactionCommitted` 事件。
- `pkg/agent/loop.go` — 每轮触发 `MaybeCompact` 并发事件。
- `pkg/agent/compact_test.go` — 触发测试。
- `pkg/agentsetup/setup.go` — 传 `WithMinRecent`。
- `cmd/lcoder/{main.go,commands.go}` — 落盘处理 `CompactionCommitted`→`Replace`;移除 fork/clone 命令。
- `pkg/tui/{events.go,keys.go,menu.go,sessionpicker.go,model_test.go}` — 压缩提示 + 删除 fork。

任务顺序保证每次提交都能 `go build ./...` 通过且测试绿。

---

## Task 1: 给 Session 增加原子 Replace

**Files:**
- Modify: `pkg/session/store.go`
- Test: `pkg/session/replace_test.go`

- [ ] **Step 1: 写失败测试**

创建 `pkg/session/replace_test.go`:

```go
package session

import (
	"os"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

// Replace 必须把磁盘记录整体重写为给定消息(丢弃原有更长的历史),
// 这是压缩提交时"重置 session 对应的对话记录"的落盘原语。
func TestReplaceRewritesSessionToGivenMessages(t *testing.T) {
	dir, err := os.MkdirTemp("", "lcoder-replace-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	store := NewStore(dir)
	sess, err := store.Create("/project")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := sess.Append(models.UserMessage("m")); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	summary := models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: "[Summary] earlier"}).
		WithMetadata("compacted", true)
	tail := models.UserMessage("latest")
	if err := sess.Replace([]models.AgentMessage{summary, tail}); err != nil {
		t.Fatalf("replace: %v", err)
	}

	loaded, err := store.Load(sess.Path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("expected 2 messages after replace, got %d", len(loaded.Messages))
	}
	if loaded.Messages[0].Text() != "[Summary] earlier" || loaded.Messages[1].Text() != "latest" {
		t.Fatalf("unexpected messages after replace: %+v", loaded.Messages)
	}
}
```

- [ ] **Step 2: 运行,确认失败**

Run: `go test ./pkg/session/ -run TestReplaceRewritesSessionToGivenMessages -v`
Expected: FAIL,编译错误 `sess.Replace undefined`。

- [ ] **Step 3: 实现 Replace + 原子写**

在 `pkg/session/store.go`,把现有 `Save` 改为原子写(临时文件 + rename),并新增 `Replace`。

替换现有 `Save`(store.go:210-233)为:

```go
// Save writes all messages to the session file using an atomic temp-file +
// rename so a crash mid-write cannot leave a truncated/corrupt JSONL.
func (s *Session) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), ".session-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	for _, msg := range s.Messages {
		data, err := json.Marshal(msg)
		if err != nil {
			tmp.Close()
			return err
		}
		if _, err := tmp.Write(append(data, '\n')); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.Path)
}

// Replace overwrites the session's entire conversation with msgs and persists
// it. Used when compaction commits: the runtime context (summary + recent tail)
// becomes the new on-disk state and the older raw messages are discarded.
func (s *Session) Replace(msgs []models.AgentMessage) error {
	s.Messages = append([]models.AgentMessage(nil), msgs...)
	return s.Save()
}
```

- [ ] **Step 4: 运行,确认通过**

Run: `go test ./pkg/session/ -v`
Expected: PASS(含既有用例)。

- [ ] **Step 5: 提交**

```bash
git add pkg/session/store.go pkg/session/replace_test.go
git commit -m "feat(session): add atomic Replace to overwrite session with compacted state

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 2: 新增 CompactionCommitted 事件

**Files:**
- Modify: `pkg/events/types.go`

- [ ] **Step 1: 添加事件类型与结构**

在 `pkg/events/types.go` 的常量块(types.go:12-25)末尾 `Error` 之后追加一行:

```go
	CompactionCommitted EventType = "compaction_committed"
```

在 `ErrorEvent`(types.go:102-106)之后追加:

```go
// CompactionCommittedEvent signals that the context manager folded older
// messages into a summary and committed the compacted window in place. The
// persistence layer reacts by rewriting the session to the compacted state.
type CompactionCommittedEvent struct{ Base }
```

- [ ] **Step 2: 编译验证**

Run: `go build ./pkg/events/`
Expected: 无错误。

- [ ] **Step 3: 提交**

```bash
git add pkg/events/types.go
git commit -m "feat(events): add CompactionCommitted event

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 3: Manager.MaybeCompact + WithMinRecent + SetMessages 摘要健壮性

**Files:**
- Modify: `pkg/contextmgr/manager.go`
- Test: `pkg/contextmgr/manager_compact_test.go`

- [ ] **Step 1: 写失败测试**

创建 `pkg/contextmgr/manager_compact_test.go`:

```go
package contextmgr

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func bigRecent(n int) []models.AgentMessage {
	var msgs []models.AgentMessage
	for i := 0; i < n; i++ {
		msgs = append(msgs, models.UserMessage(strings.Repeat("u", 200)))
		msgs = append(msgs, models.AssistantMessage(strings.Repeat("a", 200)))
	}
	return msgs
}

// 超过 CompactLimit 时,MaybeCompact 折叠较早消息为一条摘要并原地回写,
// recent 头部恰为一条 compacted 摘要,且最后一条 user 仍在尾巴内。
func TestMaybeCompactCommitsAndFolds(t *testing.T) {
	mgr := NewManager(TokenBudget{MaxTotal: 4000, TargetTotal: 1000, ReserveOutput: 200},
		WithSummarizer(func(msgs []models.AgentMessage) (string, error) {
			return "folded summary", nil
		}),
		WithMinRecent(4),
	)
	mgr.SetBlock(NewBlock(BlockRecent, "recent", StabilityDynamic, 100, bigRecent(20)...))

	committed, err := mgr.MaybeCompact()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !committed {
		t.Fatal("expected compaction to commit when over CompactLimit")
	}
	recent, _ := mgr.GetBlock(BlockRecent, "recent")
	if len(recent.Messages) == 0 {
		t.Fatal("recent block empty after compaction")
	}
	head := recent.Messages[0]
	if head.Role != models.RoleSystem {
		t.Fatalf("expected summary system message at head, got %v", head.Role)
	}
	if v, ok := head.Metadata["compacted"].(bool); !ok || !v {
		t.Fatal("head must be a compacted summary")
	}
	if !strings.Contains(head.Text(), "folded summary") {
		t.Fatalf("summary text not present: %q", head.Text())
	}
	// 只有一条摘要。
	count := 0
	for _, m := range recent.Messages {
		if v, ok := m.Metadata["compacted"].(bool); ok && v {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one summary, got %d", count)
	}
}

// 第二次压缩把已有摘要折叠进新摘要(滚动),摘要仍恒为一条。
func TestMaybeCompactRollingFold(t *testing.T) {
	calls := 0
	mgr := NewManager(TokenBudget{MaxTotal: 4000, TargetTotal: 1000, ReserveOutput: 200},
		WithSummarizer(func(msgs []models.AgentMessage) (string, error) {
			calls++
			// 第二次调用的输入里必须包含上一条摘要(滚动折叠)。
			if calls == 2 {
				var sawSummary bool
				for _, m := range msgs {
					if v, ok := m.Metadata["compacted"].(bool); ok && v {
						sawSummary = true
					}
				}
				if !sawSummary {
					t.Error("second compaction must fold the prior summary")
				}
			}
			return "summary", nil
		}),
		WithMinRecent(4),
	)
	mgr.SetBlock(NewBlock(BlockRecent, "recent", StabilityDynamic, 100, bigRecent(20)...))
	if c, _ := mgr.MaybeCompact(); !c {
		t.Fatal("first compaction should commit")
	}
	// 再灌入新消息,触发第二次压缩。
	recent, _ := mgr.GetBlock(BlockRecent, "recent")
	recent.Messages = append(recent.Messages, bigRecent(20)...)
	if c, _ := mgr.MaybeCompact(); !c {
		t.Fatal("second compaction should commit")
	}
	if calls != 2 {
		t.Fatalf("expected 2 summarizer calls, got %d", calls)
	}
}

// 未超阈值或无 summarizer 时不动。
func TestMaybeCompactNoopBelowThreshold(t *testing.T) {
	mgr := NewManager(TokenBudget{MaxTotal: 100000, TargetTotal: 100000, ReserveOutput: 200},
		WithSummarizer(func(msgs []models.AgentMessage) (string, error) { return "x", nil }),
		WithMinRecent(4),
	)
	mgr.SetBlock(NewBlock(BlockRecent, "recent", StabilityDynamic, 100, bigRecent(2)...))
	if c, _ := mgr.MaybeCompact(); c {
		t.Fatal("should not compact below threshold")
	}

	nosum := NewManager(TokenBudget{MaxTotal: 100, TargetTotal: 10, ReserveOutput: 0}, WithMinRecent(4))
	nosum.SetBlock(NewBlock(BlockRecent, "recent", StabilityDynamic, 100, bigRecent(20)...))
	if c, _ := nosum.MaybeCompact(); c {
		t.Fatal("should not compact without a summarizer")
	}
}

// 重载含 compacted 摘要的消息时,摘要保留在 recent(不被上提为系统提示词),
// 且已存在的系统提示词不被清空。
func TestSetMessagesKeepsCompactedSummaryInRecent(t *testing.T) {
	mgr := NewManager(TokenBudget{MaxTotal: 100000, TargetTotal: 100000, ReserveOutput: 0})
	mgr.SetSystemPrompt("PERSONA")

	summary := models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: "[Summary] x"}).
		WithMetadata("compacted", true)
	mgr.SetMessages([]models.AgentMessage{summary, models.UserMessage("hi")})

	if mgr.SystemPrompt() != "PERSONA" {
		t.Fatalf("system prompt must be preserved, got %q", mgr.SystemPrompt())
	}
	recent, ok := mgr.GetBlock(BlockRecent, "recent")
	if !ok || len(recent.Messages) != 2 {
		t.Fatalf("expected summary+user in recent, got %+v", recent)
	}
	if v, ok := recent.Messages[0].Metadata["compacted"].(bool); !ok || !v {
		t.Fatal("compacted summary must remain in recent, not hoisted to system")
	}
}
```

- [ ] **Step 2: 运行,确认失败**

Run: `go test ./pkg/contextmgr/ -run 'TestMaybeCompact|TestSetMessagesKeeps' -v`
Expected: FAIL,`WithMinRecent` / `MaybeCompact` undefined。

- [ ] **Step 3: 实现 WithMinRecent + keepRecent 字段**

在 `pkg/contextmgr/manager.go` 的 `Manager` 结构体(manager.go:52-59)增加字段:

```go
	policy     WindowPolicy
	keepRecent int
```

在 `NewManager`(manager.go:80-91)的默认初始化里设默认值,把:

```go
	m := &Manager{
		budget:     budget,
		estimator:  DefaultEstimator,
		summarizer: nil,
		policy:     &KeepRecentInBudget{},
	}
```

改为:

```go
	m := &Manager{
		budget:     budget,
		estimator:  DefaultEstimator,
		summarizer: nil,
		policy:     &KeepRecentInBudget{},
		keepRecent: 10,
	}
```

在 `WithWindowPolicy`(manager.go:75-77)之后新增 Option:

```go
// WithMinRecent sets the minimum number of recent messages MaybeCompact retains
// (alongside the last user message) when folding older messages into a summary.
func WithMinRecent(n int) Option {
	return func(m *Manager) {
		if n < 1 {
			n = 1
		}
		m.keepRecent = n
	}
}
```

- [ ] **Step 4: 实现 MaybeCompact**

在 `pkg/contextmgr/manager.go` 的 `ReplaceRecent`(manager.go:236-238)之后新增:

```go
// MaybeCompact folds the older portion of the recent block into a single summary
// message when the estimated total exceeds CompactLimit and a summarizer is
// configured. It mutates the recent block in place and reports whether a
// compaction was committed. A prior summary at the head of the recent block is
// part of the folded older slice (rolling compaction), so the recent block holds
// at most one summary. A summarizer error is returned without mutating state so
// the caller can treat it as non-fatal.
func (m *Manager) MaybeCompact() (bool, error) {
	if m.summarizer == nil {
		return false, nil
	}
	total := 0
	for _, b := range m.blocks {
		total += m.EstimateTokens(b.Messages)
	}
	if total <= m.budget.CompactLimit() {
		return false, nil
	}
	recent, ok := m.GetBlock(BlockRecent, "recent")
	if !ok || len(recent.Messages) == 0 {
		return false, nil
	}

	keep := m.keepRecent
	if keep < 1 {
		keep = 1
	}
	if keep > len(recent.Messages) {
		keep = len(recent.Messages)
	}
	// Ensure the last user message stays within the retained tail.
	lastUserIdx := -1
	for i := len(recent.Messages) - 1; i >= 0; i-- {
		if recent.Messages[i].Role == models.RoleUser {
			lastUserIdx = i
			break
		}
	}
	if lastUserIdx >= 0 && lastUserIdx < len(recent.Messages)-keep {
		keep = len(recent.Messages) - lastUserIdx
	}

	older := recent.Messages[:len(recent.Messages)-keep]
	tail := stripLeadingOrphanToolResults(recent.Messages[len(recent.Messages)-keep:])
	if len(older) == 0 {
		return false, nil
	}

	summaryText, err := m.summarizer(older)
	if err != nil {
		return false, err
	}
	summary := models.NewAgentMessage(models.RoleSystem, models.TextContent{
		Text: "[Summary of earlier conversation]\n\n" + summaryText,
	}).WithMetadata("compacted", true)

	m.ReplaceRecent(append([]models.AgentMessage{summary}, tail...))
	return true, nil
}
```

> 说明:`stripLeadingOrphanToolResults` 已定义于同包 `window.go`,直接复用。

- [ ] **Step 5: 修 SetMessages 的摘要健壮性 + 不清空系统块**

把 `pkg/contextmgr/manager.go` 现有 `SetMessages`(manager.go:266-282)整体替换为:

```go
// SetMessages rebuilds the conversation from a flat message list. Genuine system
// messages become the system prompt; compacted summaries (metadata compacted=true)
// stay in the recent block so a reloaded runtime state keeps its summary. The
// existing system block is left intact when msgs carry no genuine system message,
// so reloading a session never wipes the persona/system prompt.
func (m *Manager) SetMessages(msgs []models.AgentMessage) {
	var nonSystem []models.AgentMessage
	for _, msg := range msgs {
		if msg.Role == models.RoleSystem && !isCompactedSummary(msg) {
			m.SetSystemPrompt(msg.Text())
		} else {
			nonSystem = append(nonSystem, msg)
		}
	}
	m.ReplaceRecent(nonSystem)
}

func isCompactedSummary(msg models.AgentMessage) bool {
	if msg.Metadata == nil {
		return false
	}
	v, ok := msg.Metadata["compacted"].(bool)
	return ok && v
}
```

- [ ] **Step 6: 运行新测试,确认通过**

Run: `go test ./pkg/contextmgr/ -run 'TestMaybeCompact|TestSetMessagesKeeps' -v`
Expected: PASS。

- [ ] **Step 7: 跑全包,修可能受 SetMessages 改动影响的既有测试**

Run: `go test ./pkg/contextmgr/ -v`
Expected: PASS。若某用例断言"SetMessages 后存在空系统块",改为断言系统块内容被保留(本改动不再创建空系统块)。

- [ ] **Step 8: 提交**

```bash
git add pkg/contextmgr/manager.go pkg/contextmgr/manager_compact_test.go
git commit -m "feat(contextmgr): committed MaybeCompact with rolling fold; keep summaries in recent on reload

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 4: agent loop 每轮触发 MaybeCompact 并发事件

**Files:**
- Modify: `pkg/agent/loop.go`
- Test: `pkg/agent/compact_test.go`

- [ ] **Step 1: 写失败测试**

创建 `pkg/agent/compact_test.go`:

```go
package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
)

// agent 在每轮前调用 mgr.MaybeCompact;提交时发 CompactionCommitted 事件。
func TestAgentEmitsCompactionCommitted(t *testing.T) {
	mgr := contextmgr.NewManager(
		contextmgr.TokenBudget{MaxTotal: 4000, TargetTotal: 800, ReserveOutput: 100},
		contextmgr.WithSummarizer(func(msgs []models.AgentMessage) (string, error) { return "s", nil }),
		contextmgr.WithMinRecent(2),
	)
	var recent []models.AgentMessage
	for i := 0; i < 20; i++ {
		recent = append(recent, models.UserMessage(strings.Repeat("u", 200)))
		recent = append(recent, models.AssistantMessage(strings.Repeat("a", 200)))
	}
	mgr.SetBlock(contextmgr.NewBlock(contextmgr.BlockRecent, "recent", contextmgr.StabilityDynamic, 100, recent...))

	a := &Agent{mgr: mgr, bus: events.New()}
	var got bool
	unsub := a.bus.Subscribe(func(ctx context.Context, ev events.Event) error {
		if _, ok := ev.(events.CompactionCommittedEvent); ok {
			got = true
		}
		return nil
	})
	defer unsub()

	a.maybeCompact(context.Background(), 1)
	if !got {
		t.Fatal("expected CompactionCommitted event to be emitted")
	}
}
```

- [ ] **Step 2: 运行,确认失败**

Run: `go test ./pkg/agent/ -run TestAgentEmitsCompactionCommitted -v`
Expected: FAIL,`a.maybeCompact undefined`。

- [ ] **Step 3: 实现 maybeCompact 辅助并接入 run 循环**

在 `pkg/agent/loop.go` 的 `appendMessage`(loop.go:329-333)之后新增:

```go
// maybeCompact asks the context manager to commit a compaction at a turn
// boundary. On commit it emits CompactionCommitted so the persistence layer can
// rewrite the session to the compacted state. A summarizer error is non-fatal:
// it surfaces as an Error event and the turn proceeds with the truncation
// backstop in BuildTurnRequest.
func (a *Agent) maybeCompact(ctx context.Context, turn int) {
	a.mu.Lock()
	committed, err := a.mgr.MaybeCompact()
	a.mu.Unlock()
	if err != nil {
		_ = a.bus.Emit(ctx, events.ErrorEvent{
			Base:    events.Base{Type: events.Error, Turn: turn},
			Message: "compaction: " + err.Error(),
		})
		return
	}
	if committed {
		_ = a.bus.Emit(ctx, events.CompactionCommittedEvent{
			Base: events.Base{Type: events.CompactionCommitted, Turn: turn},
		})
	}
}
```

在 `run` 的 for 循环里,把 `streamAssistant` 调用之前(loop.go:277-279,`TurnStartEvent` 之后)插入一行,使该段变为:

```go
		_ = a.bus.Emit(ctx, events.TurnStartEvent{Base: events.Base{Type: events.TurnStart, Turn: turn}})

		a.maybeCompact(ctx, turn)

		assistantMsg, err := a.streamAssistant(ctx, turn)
```

- [ ] **Step 4: 运行,确认通过**

Run: `go test ./pkg/agent/ -run TestAgentEmitsCompactionCommitted -v`
Expected: PASS。

- [ ] **Step 5: 跑全包**

Run: `go test ./pkg/agent/ -v`
Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add pkg/agent/loop.go pkg/agent/compact_test.go
git commit -m "feat(agent): trigger committed compaction each turn and emit CompactionCommitted

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 5: agentsetup 传入 MinRecent

**Files:**
- Modify: `pkg/agentsetup/setup.go`

- [ ] **Step 1: 接线 WithMinRecent**

在 `pkg/agentsetup/setup.go` 的 `NewContextManager` 里,把 opts 初始化(setup.go:41-43):

```go
	opts := []contextmgr.Option{
		contextmgr.WithWindowPolicy(contextmgr.NewKeepRecentInBudget(cfg.Context.MinRecent)),
	}
```

改为:

```go
	opts := []contextmgr.Option{
		contextmgr.WithWindowPolicy(contextmgr.NewKeepRecentInBudget(cfg.Context.MinRecent)),
		contextmgr.WithMinRecent(cfg.Context.MinRecent),
	}
```

- [ ] **Step 2: 编译 + 测试**

Run: `go test ./pkg/agentsetup/ -v`
Expected: PASS。

- [ ] **Step 3: 提交**

```bash
git add pkg/agentsetup/setup.go
git commit -m "feat(agentsetup): pass MinRecent into context manager for MaybeCompact

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 6: 落盘层处理 CompactionCommitted → Replace

**Files:**
- Modify: `cmd/lcoder/main.go`

- [ ] **Step 1: runTUI 的 persistHandler 处理压缩重写**

在 `cmd/lcoder/main.go` 的 `runTUI`(main.go:402-408)把:

```go
	persistHandler := func(ctx context.Context, ev events.Event) error {
		switch ev.(type) {
		case events.MessageEndEvent, events.ToolExecutionEndEvent, events.AgentEndEvent:
			_ = setup.sess.Save()
		}
		return nil
	}
```

改为:

```go
	persistHandler := func(ctx context.Context, ev events.Event) error {
		switch ev.(type) {
		case events.CompactionCommittedEvent:
			// Compaction committed in the manager: reset the on-disk session to
			// the compacted runtime state (summary + recent tail), discarding the
			// older raw messages.
			_ = setup.sess.Replace(setup.ag.AllMessages())
		case events.MessageEndEvent, events.ToolExecutionEndEvent, events.AgentEndEvent:
			_ = setup.sess.Save()
		}
		return nil
	}
```

- [ ] **Step 2: runOneShot 的 persistHandler 同样处理**

在 `runOneShot`(main.go:334-340)把:

```go
	persistHandler := func(ctx context.Context, ev events.Event) error {
		switch ev.(type) {
		case events.MessageEndEvent, events.ToolExecutionEndEvent, events.AgentEndEvent:
			_ = setup.sess.Save()
		}
		return nil
	}
```

改为:

```go
	persistHandler := func(ctx context.Context, ev events.Event) error {
		switch ev.(type) {
		case events.CompactionCommittedEvent:
			_ = setup.sess.Replace(setup.ag.AllMessages())
		case events.MessageEndEvent, events.ToolExecutionEndEvent, events.AgentEndEvent:
			_ = setup.sess.Save()
		}
		return nil
	}
```

- [ ] **Step 3: 编译**

Run: `go build ./...`
Expected: 无错误。

- [ ] **Step 4: 提交**

```bash
git add cmd/lcoder/main.go
git commit -m "feat(cmd): rewrite session to compacted state on CompactionCommitted

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 7: TUI 压缩提示行

**Files:**
- Modify: `pkg/tui/events.go`

- [ ] **Step 1: 在 handleEvent 增加 CompactionCommitted 分支**

在 `pkg/tui/events.go` 的 `handleEvent` switch 中,`ErrorEvent`(events.go:77-79)分支之前插入:

```go
	case events.CompactionCommittedEvent:
		m.addSystem("↧ 已压缩早前对话以节省 token(原始记录已合并为摘要)")

```

- [ ] **Step 2: 编译 + 测试**

Run: `go test ./pkg/tui/ -v`
Expected: PASS(编译通过即可,无新断言)。

- [ ] **Step 3: 提交**

```bash
git add pkg/tui/events.go
git commit -m "feat(tui): show a notice line when context is compacted

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 8: window policy 退化为截断兜底

**Files:**
- Modify: `pkg/contextmgr/window.go`
- Modify: `pkg/contextmgr/window_test.go`

- [ ] **Step 1: 改写 Apply 只做截断兜底**

把 `pkg/contextmgr/window.go` 的 `Apply`(window.go:34-50)整体替换为:

```go
// Apply selects blocks within the token budget. Compaction is now a committed
// operation (Manager.MaybeCompact) run at turn boundaries, so the window policy's
// sole remaining job is a truncation backstop: keep static/stable blocks and drop
// the head of dynamic blocks so the request never exceeds the hard input limit,
// even when compaction was skipped or failed.
func (p *KeepRecentInBudget) Apply(blocks []*Block, budget TokenBudget, mgr *Manager) ([]*Block, error) {
	return p.fitWithoutCompaction(blocks, budget, mgr)
}
```

- [ ] **Step 2: 删除不再使用的 summarizer 压缩代码**

删除 `pkg/contextmgr/window.go` 中的 `fitWithCompaction`(window.go:75-122)与 `compactRecent`(window.go:124-167)两个函数整体。保留 `fitWithoutCompaction`、`keepTail`、`stripLeadingOrphanToolResults`、`ensureLastUser`。

- [ ] **Step 3: 更新 window_test.go**

`pkg/contextmgr/window_test.go` 的 `TestWindowCompactionFallsBackOnSummarizerError`(window_test.go:50-98)现在语义是"window 不再压缩,直接截断"。把该测试函数整体替换为:

```go
// The window policy no longer summarizes; it only truncates to fit the hard
// limit. Even with a summarizer configured on the manager, BuildTurnRequest must
// inject no compacted summary and must keep the request within budget.
func TestWindowTruncatesWithoutSummarizing(t *testing.T) {
	mgr := NewManager(TokenBudget{
		MaxTotal:      2000,
		TargetTotal:   1500,
		ReserveOutput: 500,
	}, WithSummarizer(func(msgs []models.AgentMessage) (string, error) {
		return "should not be called by window policy", nil
	}), WithWindowPolicy(DefaultKeepRecentInBudget()))

	mgr.SetBlock(NewBlock(BlockSystem, "system", StabilityStatic, 100,
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: strings.Repeat("a", 800)}),
	))

	var recent []models.AgentMessage
	for i := 0; i < 20; i++ {
		recent = append(recent, models.UserMessage(strings.Repeat("u", 200)))
		recent = append(recent, models.AssistantMessage(strings.Repeat("a", 200)))
	}
	mgr.SetBlock(NewBlock(BlockRecent, "recent", StabilityDynamic, 100, recent...))

	req, err := mgr.BuildTurnRequest(models.ModelRef{Provider: "openai", ID: "gpt-4o"}, nil)
	if err != nil {
		t.Fatalf("expected graceful truncation, got error: %v", err)
	}
	if len(req.Messages) == 0 {
		t.Fatal("expected truncated messages to remain")
	}
	for _, m := range req.Messages {
		if m.Role == models.RoleSystem {
			if v, ok := m.Metadata["compacted"].(bool); ok && v {
				t.Fatal("window policy must not inject a compacted summary")
			}
		}
	}
	var foundUser bool
	for _, m := range req.Messages {
		if m.Role == models.RoleUser {
			foundUser = true
			break
		}
	}
	if !foundUser {
		t.Fatal("expected at least one user message after truncation")
	}
}
```

> `errors` import 若因删除该用例而不再使用,从 `window_test.go` 的 import 块移除 `"errors"`。

- [ ] **Step 4: 运行**

Run: `go test ./pkg/contextmgr/ -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add pkg/contextmgr/window.go pkg/contextmgr/window_test.go
git commit -m "refactor(contextmgr): window policy is truncation-only; compaction now committed

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 9: 删除 session tree(fork/clone/分支)

**Files:**
- Delete: `pkg/session/tree.go`, `pkg/session/tree_test.go`
- Modify: `cmd/lcoder/main.go`, `cmd/lcoder/commands.go`
- Modify: `pkg/tui/sessionpicker.go`, `pkg/tui/keys.go`, `pkg/tui/menu.go`, `pkg/tui/model_test.go`

- [ ] **Step 1: 删除 tree.go 与 tree_test.go**

```bash
git rm pkg/session/tree.go pkg/session/tree_test.go
```

- [ ] **Step 2: 移除 cmd 的 fork/clone 命令**

在 `cmd/lcoder/main.go` 删除注册行(main.go:64-65):

```go
	root.AddCommand(forkCmd())
	root.AddCommand(cloneCmd())
```

在 `cmd/lcoder/commands.go` 删除 `forkCmd`(commands.go:89-117)与 `cloneCmd`(commands.go:119-145)两个函数整体。

- [ ] **Step 3: TUI 去掉 fork**

`pkg/tui/sessionpicker.go`:从 `SessionStore` 接口(sessionpicker.go:25-29)删除 `Fork` 一行,并删除 `ForkCurrent` 方法(sessionpicker.go:114-120)。`mode` 字段与 `if mode == "fork"` 标题分支(sessionpicker.go:56-58)保留无害,但可一并简化:把该 if 块删除,标题恒为 `"Sessions"`。

`pkg/tui/keys.go`:把 `case "sessions", "fork":`(keys.go:494)改为 `case "sessions":`。

`pkg/tui/menu.go`:删除 `{Name: "fork", Description: "Fork session", Category: "Session"},`(menu.go:23)。

`pkg/tui/model_test.go`:删除 `fakeSessionStore.Fork` 方法(model_test.go:68 附近整段方法)。

- [ ] **Step 4: 编译**

Run: `go build ./...`
Expected: 无错误。若报 `Fork` 仍被引用,grep `Fork(` 定位残留并清除。

- [ ] **Step 5: 测试**

Run: `go test ./cmd/... ./pkg/tui/... ./pkg/session/... -v`
Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add -A
git commit -m "refactor: remove session tree (fork/clone/branching) for a linear session model

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 10: session 线性化(去掉 ParentID/buildBranch/ActiveBranch)

**Files:**
- Modify: `pkg/session/store.go`, `pkg/session/store_test.go`

- [ ] **Step 1: 更新 store_test.go**

`pkg/session/store_test.go` 删除 ActiveBranch 断言(store_test.go:38-40):

```go
	if len(loaded.ActiveBranch) != 1 {
		t.Fatalf("expected active branch length 1, got %d", len(loaded.ActiveBranch))
	}
```

- [ ] **Step 2: 线性化 store.go**

`pkg/session/store.go` 改动:

1. `Session` 结构体(store.go:47-54)删除 `ActiveBranch []string` 字段。
2. `Create`(store.go:57-74)删除 `ActiveBranch: []string{},` 初始化行。
3. `Load`(store.go:111-115)删除重建分支段:

```go
	// Rebuild active branch from root to last message without branching.
	if len(sess.Messages) > 0 {
		last := sess.Messages[len(sess.Messages)-1]
		sess.ActiveBranch = buildBranch(sess.Messages, last.ID)
	}
```

4. `Append`(store.go:190-207)删除 ParentID 标记与 ActiveBranch 重建,使其变为:

```go
// Append adds a message to the session and persists it.
func (s *Session) Append(msg models.AgentMessage) error {
	if msg.Metadata == nil {
		msg.Metadata = make(map[string]any)
	}
	msg.Metadata["session_id"] = s.ID
	msg.Metadata["cwd"] = s.CWD
	msg.Metadata["saved_at"] = time.Now().UnixMilli()

	s.Messages = append(s.Messages, msg)
	return s.Save()
}
```

5. `ActiveMessages`(store.go:235-248)简化为纯线性返回:

```go
// ActiveMessages returns the session's messages in order. The session is a
// linear conversation; this remains as the accessor used by reload paths.
func (s *Session) ActiveMessages() []models.AgentMessage {
	return s.Messages
}
```

6. 删除 `buildBranch` 函数(store.go:262-282)整体。

- [ ] **Step 3: 编译**

Run: `go build ./...`
Expected: 无错误(`buildBranch` 已无引用——Task 9 已删除唯一外部使用方 tree.go)。

- [ ] **Step 4: 测试**

Run: `go test ./pkg/session/... ./cmd/... ./pkg/tui/... -v`
Expected: PASS。

- [ ] **Step 5: 全量回归**

Run: `go build ./... && go test ./...`
Expected: 全绿。

- [ ] **Step 6: 提交**

```bash
git add pkg/session/store.go pkg/session/store_test.go
git commit -m "refactor(session): linear message model, drop ParentID/buildBranch/ActiveBranch

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Self-Review(规划者自查)

**Spec 覆盖核对:**
- 运行时状态=recent 块=JSONL,单状态 → Task 3(MaybeCompact 原地)、Task 6(Replace 落盘)、Task 10(线性模型)。✓
- 压缩前实时落盘 → 既有 `AppendMissing`(TUI `persistSession` / oneShot 末尾)保留,未改动。✓
- 压缩一次性提交 + 滚动折叠 → Task 3 `MaybeCompact` + `TestMaybeCompactRollingFold`。✓
- 压缩时重置 session、丢弃老消息 → Task 1 `Replace` + Task 6 事件处理。✓
- 重启恢复压缩态 → Task 3 `SetMessages` 摘要健壮性 + `NewModel`/`NewContextManager` 既有重建路径;`TestSetMessagesKeepsCompactedSummaryInRecent`。✓
- window 退化为截断兜底 → Task 8。✓
- 删除 fork/clone/tree → Task 9 + Task 10。✓
- 触发时机/事件/TUI 提示 → Task 2、Task 4、Task 7。✓
- 原子写硬化 → Task 1 `Save` temp+rename。✓
- AutoCompact 关闭降级 → `MaybeCompact` 在 `summarizer==nil` 时 no-op(`TestMaybeCompactNoopBelowThreshold`),window 截断兜底。✓

**占位符扫描:** 无 TBD/TODO;每个代码步骤含完整代码。✓

**类型一致性:** `MaybeCompact() (bool, error)`、`WithMinRecent(int)`、`Session.Replace([]models.AgentMessage) error`、`CompactionCommittedEvent`、`a.maybeCompact(ctx, turn)`、`isCompactedSummary` —— 跨任务引用一致。✓

**已知顺序约束:** Task 9 必须在 Task 10 之前(tree.go 是 `buildBranch` 的外部使用方);Task 4 在 Task 8 之前(先接入 MaybeCompact 再撤掉 window 压缩,避免出现完全无压缩的中间提交)。✓
