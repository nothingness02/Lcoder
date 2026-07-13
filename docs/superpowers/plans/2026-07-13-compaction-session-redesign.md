# Compaction 与 Session 持久化重设计 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 Lcoder 的压缩机制从"按条数保留 + 破坏性 Replace"改为"token 预算切点 + split-turn 双摘要 + 可取消摘要管线 + 文件内 append-only CompactionEntry",根治五个边界问题(A/B/C/D/E)。

**Architecture:** `pkg/contextmgr` 负责切点与折叠(新文件 `fold.go`),`pkg/compaction` 负责摘要管线(序列化截断 + ctx 贯穿),`pkg/session` 负责 CompactionEntry 追加与 EffectiveMessages 视图重建,`pkg/agent`/`cmd/lcoder` 负责事件载荷与持久化切换,`pkg/config`/`pkg/agentsetup` 负责配置对齐。TDD:每个任务先写失败测试。

**Tech Stack:** Go 1.25,标准库为主;测试 `go test`,回归命令 `go test $(go list ./... | grep -v 'reference/Shannon')`。

**设计文档:** `docs/superpowers/specs/2026-07-13-compaction-session-redesign-design.md`

---

## 文件结构

| 文件 | 责任 |
|------|------|
| `pkg/compaction/summarizer.go` | `SummarizeFunc` 类型(加 ctx)、`NewLLMSummarizer`(序列化输入 + ctx 派生 timeout) |
| `pkg/compaction/serialize.go`(新建) | `SerializeConversation`:消息→纯文本,tool result 截断 2000 字符,总量二次截断 |
| `pkg/compaction/compaction.go` | `KeepRecent.Compact`/`SimpleSummarize` 加 ctx |
| `pkg/compaction/breaker.go` | `Wrap` 加 ctx 透传 |
| `pkg/contextmgr/fold.go`(新建) | `findCutPoint`(token 预算切点)、`foldOlder`(含 split turn、降级路径)、`FoldResult` |
| `pkg/contextmgr/manager.go` | `SummarizeFunc` 类型、`keepRecentTokens` 字段与 `WithKeepRecentTokens`、删除旧 `foldOlder` |
| `pkg/contextmgr/levels.go` | `keepForLevel` 改为返回 token 预算;`MaybeCompactLeveled` 新签名 |
| `pkg/events/types.go` | `CompactionCommittedEvent` 增加载荷字段 |
| `pkg/agent/loop.go` | `maybeCompact` 传 ctx、填充事件载荷、cancel 静默 |
| `pkg/session/store.go` | `IsCompactionEntry`、`AppendCompactionEntry`、`EffectiveMessages`、`Replace` 标 deprecated |
| `cmd/lcoder/main.go` | 两处持久化订阅改用 `AppendCompactionEntry`;启动加载改 `EffectiveMessages` |
| `pkg/config/config.go` / `config_validate.go` | `keep_recent_tokens` 配置项 |
| `pkg/agentsetup/setup.go` | 传入 `WithKeepRecentTokens` |
| `configs/lcoder.yaml`、`eval/swe-bench-lite/config/lcoder.yaml` | 配置对齐 |

---

### Task 1: SummarizeFunc 签名加 context

**Files:**
- Modify: `pkg/compaction/compaction.go:19`、`pkg/compaction/summarizer.go:46-47`、`pkg/compaction/breaker.go:43-44`、`pkg/contextmgr/manager.go:88`、`pkg/agentsetup/setup.go:56`
- Test(机械修改): `pkg/contextmgr/manager_compact_test.go`、`pkg/agent/compact_test.go`、`pkg/agent/loop_test.go`、`pkg/agent/checkpoint_test.go`、`pkg/compaction/summarizer_test.go`、`pkg/compaction/breaker_test.go`、`test/integration/compaction_test.go`、`test/integration/contexteng_test.go`

- [ ] **Step 1: 修改 `pkg/compaction/compaction.go` 的类型与两个函数**

`SummarizeFunc` 加 ctx;`KeepRecent.Compact` 加 ctx 透传;`SimpleSummarize` 加 ctx(忽略):

```go
// SummarizeFunc generates a summary from a slice of messages.
// In production this calls the LLM engine. The context carries the agent's
// run cancellation so abort/Ctrl+C interrupts in-flight summarization.
type SummarizeFunc func(ctx context.Context, messages []models.AgentMessage) (string, error)

// Compact summarizes older messages and appends a system message with the summary.
func (k *KeepRecent) Compact(ctx context.Context, messages []models.AgentMessage, summarize SummarizeFunc) ([]models.AgentMessage, error) {
	if len(messages) <= k.KeepCount {
		return messages, nil
	}

	older := messages[:len(messages)-k.KeepCount]
	recent := messages[len(messages)-k.KeepCount:]

	summaryText, err := summarize(ctx, older)
	if err != nil {
		return nil, fmt.Errorf("summarize: %w", err)
	}
	// ...(其余不变)
}

// SimpleSummarize is a placeholder summarizer.
func SimpleSummarize(_ context.Context, messages []models.AgentMessage) (string, error) {
	// ...(body 不变)
}
```

文件顶部 import 增加 `"context"`。

- [ ] **Step 2: 修改 `pkg/compaction/breaker.go` 的 `Wrap`**

```go
// Wrap returns a SummarizeFunc that short-circuits when the breaker is OPEN and
// otherwise delegates to inner, updating the failure counter from the outcome.
func (cb *CircuitBreaker) Wrap(inner SummarizeFunc) SummarizeFunc {
	return func(ctx context.Context, messages []models.AgentMessage) (string, error) {
		cb.mu.Lock()
		open := cb.failures >= cb.maxFailures
		cb.mu.Unlock()
		if open {
			return "", ErrCompactionSkipped
		}

		summary, err := inner(ctx, messages)
		// ...(计数逻辑不变)
	}
}
```

import 增加 `"context"`。

- [ ] **Step 3: 修改 `pkg/contextmgr/manager.go` 的类型与 `foldOlder` 签名**

```go
// SummarizeFunc generates a summary from messages. The context carries run
// cancellation; summarizers must honor it.
type SummarizeFunc func(ctx context.Context, messages []models.AgentMessage) (string, error)
```

`foldOlder` 临时改为(本任务只改签名,逻辑在 Task 4 重写):

```go
func (m *Manager) foldOlder(ctx context.Context, keep int) (bool, error) {
	// ...(逻辑不变,仅 m.summarizer(older) 改为 m.summarizer(ctx, older))
}
```

`MaybeCompactLeveled` 内部调用改为 `m.foldOlder(context.Background(), m.keepForLevel(level))`(Task 7 改为透传 run ctx)。import 增加 `"context"`。

- [ ] **Step 4: 修改 `pkg/compaction/summarizer.go` 的 `NewLLMSummarizer`**

签名改为接收外部 ctx 并派生 timeout(序列化改动在 Task 3,本步只改 ctx):

```go
func NewLLMSummarizer(client *llm.Client, model models.ModelRef) SummarizeFunc {
	return func(ctx context.Context, messages []models.AgentMessage) (string, error) {
		if client == nil {
			return "", fmt.Errorf("llm summarizer: nil client")
		}
		if len(messages) == 0 {
			return "No earlier messages.", nil
		}

		ctx, cancel := context.WithTimeout(ctx, summaryTimeout)
		defer cancel()
		// ...(其余不变)
	}
}
```

- [ ] **Step 5: 修改 `pkg/agentsetup/setup.go:56`**

该行不变(`compaction.NewLLMSummarizer` 返回值仍是 `SummarizeFunc`,`contextmgr.SummarizeFunc(...)` 转换仍成立——确认两个包类型定义一致后无需改动)。如编译报类型不匹配,把转换去掉直接传。

- [ ] **Step 6: 机械修复所有测试调用点**

统一模式:所有 `func(msgs []models.AgentMessage) (string, error)` 字面量改为 `func(_ context.Context, msgs []models.AgentMessage) (string, error)`;`wrapped(nil)` 改为 `wrapped(context.Background(), nil)`;`Compact(before, fn)` 改为 `Compact(context.Background(), before, fn)`;测试文件 import 增加 `"context"`。涉及文件:`pkg/contextmgr/manager_compact_test.go`(3 处)、`pkg/agent/compact_test.go`(3 处)、`pkg/agent/loop_test.go`(1 处)、`pkg/agent/checkpoint_test.go`(1 处)、`pkg/compaction/summarizer_test.go`、`pkg/compaction/breaker_test.go`、`test/integration/compaction_test.go`(5 处)、`test/integration/contexteng_test.go`。

- [ ] **Step 7: 编译 + 全量测试**

Run: `go build ./... && go test $(go list ./... | grep -v 'reference/Shannon')`
Expected: 全部通过(行为未变,仅签名变化)。

- [ ] **Step 8: Commit**

```bash
git add pkg/compaction pkg/contextmgr pkg/agentsetup pkg/agent test
git commit -m "refactor(compaction): add context to SummarizeFunc for cancellable summarization"
```

---

### Task 2: 会话序列化器(serialize.go)

**Files:**
- Create: `pkg/compaction/serialize.go`
- Test: `pkg/compaction/serialize_test.go`

- [ ] **Step 1: 写失败测试 `pkg/compaction/serialize_test.go`**

```go
package compaction

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func TestSerializeConversationRoles(t *testing.T) {
	msgs := []models.AgentMessage{
		models.UserMessage("fix the bug"),
		models.NewAgentMessage(models.RoleAssistant,
			models.TextContent{Text: "let me look"},
			models.ToolCallContent{ID: "c1", Name: "read", Arguments: map[string]any{"path": "foo.go"}}),
		models.NewAgentMessage(models.RoleToolResult, models.ToolResultContent{
			ToolCallID: "c1", Name: "read",
			Content: []models.ContentPart{models.TextContent{Text: "package main"}},
		}),
	}
	out := SerializeConversation(msgs, 2000)
	for _, want := range []string{"[User]: fix the bug", "[Assistant]: let me look", `[Assistant tool calls]: read(path="foo.go")`, "[Tool result]: package main"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestSerializeConversationTruncatesToolResults(t *testing.T) {
	big := strings.Repeat("z", 10000)
	msgs := []models.AgentMessage{
		models.NewAgentMessage(models.RoleToolResult, models.ToolResultContent{
			ToolCallID: "c1", Name: "read",
			Content: []models.ContentPart{models.TextContent{Text: big}},
		}),
	}
	out := SerializeConversation(msgs, 2000)
	if len(out) > 2200 {
		t.Fatalf("expected truncated output, got %d chars", len(out))
	}
	if !strings.Contains(out, "truncated 8000 chars") {
		t.Fatalf("missing truncation marker:\n%s", out[:200])
	}
}

func TestSerializeConversationThinking(t *testing.T) {
	msgs := []models.AgentMessage{
		models.NewAgentMessage(models.RoleAssistant, models.ThinkingContent{Text: "hmm"}),
	}
	out := SerializeConversation(msgs, 2000)
	if !strings.Contains(out, "[Assistant thinking]: hmm") {
		t.Fatalf("missing thinking line: %q", out)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/compaction -run TestSerializeConversation -v`
Expected: FAIL(undefined: SerializeConversation)。

- [ ] **Step 3: 实现 `pkg/compaction/serialize.go`**

```go
package compaction

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lcoder/lcoder/pkg/models"
)

// SerializeConversation renders messages as plain text for the summarizer,
// mirroring pi's serializeConversation: explicit role labels prevent the model
// from treating the input as a conversation to continue. Tool result text is
// truncated to maxToolResultChars so a single huge read/bash output cannot
// overflow the summarization request itself.
func SerializeConversation(msgs []models.AgentMessage, maxToolResultChars int) string {
	var b strings.Builder
	for _, m := range msgs {
		switch m.Role {
		case models.RoleUser:
			fmt.Fprintf(&b, "[User]: %s\n", m.Text())
		case models.RoleAssistant:
			if thinking := m.Thinking(); thinking != "" {
				fmt.Fprintf(&b, "[Assistant thinking]: %s\n", thinking)
			}
			if text := m.Text(); text != "" {
				fmt.Fprintf(&b, "[Assistant]: %s\n", text)
			}
			if calls := m.ToolCalls(); len(calls) > 0 {
				fmt.Fprintf(&b, "[Assistant tool calls]: %s\n", renderToolCalls(calls))
			}
		case models.RoleToolResult:
			text := m.Text()
			if maxToolResultChars > 0 && len(text) > maxToolResultChars {
				text = text[:maxToolResultChars] + fmt.Sprintf("\n...[truncated %d chars]", len(text)-maxToolResultChars)
			}
			fmt.Fprintf(&b, "[Tool result]: %s\n", text)
		default:
			if text := m.Text(); text != "" {
				fmt.Fprintf(&b, "[%s]: %s\n", m.Role, text)
			}
		}
	}
	return b.String()
}

// renderToolCalls renders calls as name(arg="value", ...) joined by "; ".
// Argument keys are sorted for deterministic output.
func renderToolCalls(calls []models.ToolCallContent) string {
	parts := make([]string, 0, len(calls))
	for _, c := range calls {
		keys := make([]string, 0, len(c.Arguments))
		for k := range c.Arguments {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		args := make([]string, 0, len(keys))
		for _, k := range keys {
			args = append(args, fmt.Sprintf("%s=%q", k, fmt.Sprint(c.Arguments[k])))
		}
		parts = append(parts, fmt.Sprintf("%s(%s)", c.Name, strings.Join(args, ", ")))
	}
	return strings.Join(parts, "; ")
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./pkg/compaction -run TestSerializeConversation -v`
Expected: PASS(3 个测试)。

- [ ] **Step 5: Commit**

```bash
git add pkg/compaction/serialize.go pkg/compaction/serialize_test.go
git commit -m "feat(compaction): add conversation serializer with tool-result truncation"
```

---

### Task 3: NewLLMSummarizer 接入序列化 + 总量二次截断

**Files:**
- Modify: `pkg/compaction/summarizer.go`
- Test: `pkg/compaction/summarizer_test.go`

- [ ] **Step 1: 写失败测试(追加到 `pkg/compaction/summarizer_test.go`)**

断言摘要请求的输入是序列化文本(单条 user 消息),且超大 tool result 被截断。
`llmtest.NewScript` 暴露 `adapter.LastRequest()`,可直接检查入参:

```go
func TestLLMSummarizerSendsSerializedTruncatedInput(t *testing.T) {
	client, adapter := llmtest.NewScript(llmtest.Turn(
		llmtest.Done(models.AssistantMessage("<summary>ok</summary>"), nil),
	))
	summarize := NewLLMSummarizer(client, models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"})

	big := strings.Repeat("x", 50000)
	msgs := []models.AgentMessage{
		models.UserMessage("do the thing"),
		models.NewAgentMessage(models.RoleToolResult, models.ToolResultContent{
			ToolCallID: "c1", Name: "bash",
			Content: []models.ContentPart{models.TextContent{Text: big}},
		}),
	}
	if _, err := summarize(context.Background(), msgs); err != nil {
		t.Fatalf("summarize: %v", err)
	}

	req := adapter.LastRequest()
	if len(req.Messages) != 1 || req.Messages[0].Role != models.RoleUser {
		t.Fatalf("expected a single synthetic user message, got %+v", req.Messages)
	}
	text := req.Messages[0].Text()
	if !strings.Contains(text, "[User]: do the thing") {
		t.Fatalf("input not serialized: %q", text[:min(200, len(text))])
	}
	if !strings.Contains(text, "truncated") {
		t.Fatal("tool result not truncated")
	}
	if len(text) > summaryMaxInputChars {
		t.Fatalf("serialized input %d chars exceeds cap %d", len(text), summaryMaxInputChars)
	}
}
```

测试文件 import 增加 `"context"`、`"strings"`。

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/compaction -run TestLLMSummarizerSendsSerializedTruncatedInput -v`
Expected: FAIL(当前输入是多条原始消息)。

- [ ] **Step 3: 修改 `pkg/compaction/summarizer.go`**

新增常量与序列化调用:

```go
// summaryToolResultChars caps one tool result inside the serialized input.
const summaryToolResultChars = 2000

// summaryMaxInputChars caps the whole serialized input (~12k tokens at 4
// chars/token), keeping the summarization request far below any model window.
const summaryMaxInputChars = 48000
```

`NewLLMSummarizer` 的请求构造改为:

```go
		serialized := SerializeConversation(messages, summaryToolResultChars)
		if len(serialized) > summaryMaxInputChars {
			serialized = serialized[:summaryMaxInputChars] + "\n...[input truncated]"
		}

		req := models.TurnRequest{
			Model:        model,
			SystemPrompt: summaryInstruction,
			Messages: []models.AgentMessage{
				models.NewAgentMessage(models.RoleUser, models.TextContent{Text: serialized}),
			},
		}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./pkg/compaction -v`
Expected: 全部 PASS(含既有 parseSummary 测试)。

- [ ] **Step 5: Commit**

```bash
git add pkg/compaction/summarizer.go pkg/compaction/summarizer_test.go
git commit -m "feat(compaction): serialize and bound summarizer input to prevent self-overflow"
```

---

### Task 4: token 预算切点(fold.go)

**Files:**
- Create: `pkg/contextmgr/fold.go`
- Modify: `pkg/contextmgr/manager.go`(`keepRecentTokens` 字段、`WithKeepRecentTokens`、删除旧 `foldOlder`)、`pkg/contextmgr/levels.go`(`keepForLevel` 改 token 预算、`MaybeCompactLeveled` 委托)
- Test: `pkg/contextmgr/fold_test.go`(新建)

- [ ] **Step 1: 写失败测试 `pkg/contextmgr/fold_test.go`**

```go
package contextmgr

import (
	"context"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

// msgAt 构造一条指定角色、指定字符数的消息(4 字符 ≈ 1 token)。
func msgAt(role models.MessageRole, chars int) models.AgentMessage {
	return models.NewAgentMessage(role, models.TextContent{Text: strings.Repeat("x", chars)})
}

// toolPair 构造一对 tool_use / tool_result。
func toolPair(id string, chars int) []models.AgentMessage {
	return []models.AgentMessage{
		models.NewAgentMessage(models.RoleAssistant,
			models.ToolCallContent{ID: id, Name: "read", Arguments: map[string]any{}}),
		models.NewAgentMessage(models.RoleToolResult, models.ToolResultContent{
			ToolCallID: id, Name: "read",
			Content: []models.ContentPart{models.TextContent{Text: strings.Repeat("r", chars)}},
		}),
	}
}

// A: 切点由 token 预算决定,保留尾部 token ≤ 预算,且切点在 user/assistant 边界。
func TestFindCutPointTokenBudget(t *testing.T) {
	var msgs []models.AgentMessage
	for i := 0; i < 8; i++ {
		msgs = append(msgs, msgAt(models.RoleUser, 400), msgAt(models.RoleAssistant, 400))
	}
	// 16 条 × 100 token = 1600 token;预算 500 → 保留约 5 条。
	m := NewManager(TokenBudget{MaxTotal: 100000, TargetTotal: 90000, ReserveOutput: 1000})
	cut, split := m.findCutPoint(msgs, 500, false)
	if split {
		t.Fatal("no split expected when last turn is small")
	}
	if cut <= 0 || cut >= len(msgs) {
		t.Fatalf("cut %d out of range", cut)
	}
	if msgs[cut].Role == models.RoleToolResult {
		t.Fatal("cut must not land before a tool_result")
	}
	if got := m.EstimateTokens(msgs[cut:]); got > 600 { // 预算 + 单条容差
		t.Fatalf("kept tail %d tokens exceeds budget 500 (with slack)", got)
	}
}

// 切点落在 tool_result 上时,向前推进到配对完整保留。
func TestFindCutPointKeepsToolPair(t *testing.T) {
	msgs := []models.AgentMessage{msgAt(models.RoleUser, 400)}
	msgs = append(msgs, toolPair("c1", 40000)...) // 巨大 tool_result 10k token
	msgs = append(msgs, msgAt(models.RoleUser, 400), msgAt(models.RoleAssistant, 400))
	m := NewManager(TokenBudget{MaxTotal: 100000, TargetTotal: 90000, ReserveOutput: 1000})
	cut, _ := m.findCutPoint(msgs, 500, false)
	if msgs[cut].Role == models.RoleToolResult {
		t.Fatalf("cut before tool_result would orphan it")
	}
	// 保留尾部必须包含完整配对或不包含任何一半。
	tail := msgs[cut:]
	for i, msg := range tail {
		if msg.Role == models.RoleToolResult && i == 0 {
			t.Fatal("tail starts with orphan tool_result")
		}
	}
}

// 条数下限:短消息场景保留至少 keepRecent 条(取保留更多的切点)。
func TestFindCutPointMessageFloor(t *testing.T) {
	var msgs []models.AgentMessage
	for i := 0; i < 20; i++ {
		msgs = append(msgs, msgAt(models.RoleUser, 40), msgAt(models.RoleAssistant, 40)) // 10 token 每条
	}
	m := NewManager(TokenBudget{MaxTotal: 100000, TargetTotal: 90000, ReserveOutput: 1000}, WithMinRecent(6))
	cut, _ := m.findCutPoint(msgs, 100, false) // token 预算只够 10 条,下限要求 6 条
	if kept := len(msgs) - cut; kept < 6 {
		t.Fatalf("message floor violated: kept %d < 6", kept)
	}
}

// 最后 user 保护:非 reactive 不切进最后一轮。
func TestFindCutPointProtectsLastTurn(t *testing.T) {
	var msgs []models.AgentMessage
	msgs = append(msgs, msgAt(models.RoleUser, 400), msgAt(models.RoleAssistant, 400))
	msgs = append(msgs, msgAt(models.RoleUser, 400))                       // 最后一轮开始
	msgs = append(msgs, toolPair("c1", 40000)...)                          // 最后一轮自身 10k token
	m := NewManager(TokenBudget{MaxTotal: 100000, TargetTotal: 90000, ReserveOutput: 1000})
	cut, split := m.findCutPoint(msgs, 500, false) // allowSplit=false
	if split {
		t.Fatal("split must not happen without allowSplit")
	}
	if cut != 2 {
		t.Fatalf("expected cut at last turn start (2), got %d", cut)
	}
}

// B: reactive 允许 split turn,切在最后一轮内部的合法边界。
func TestFindCutPointSplitTurn(t *testing.T) {
	var msgs []models.AgentMessage
	msgs = append(msgs, msgAt(models.RoleUser, 400), msgAt(models.RoleAssistant, 400))
	msgs = append(msgs, msgAt(models.RoleUser, 400)) // 最后一轮开始 idx=2
	msgs = append(msgs, toolPair("c1", 40000)...)
	msgs = append(msgs, msgAt(models.RoleAssistant, 400), msgAt(models.RoleAssistant, 400))
	m := NewManager(TokenBudget{MaxTotal: 100000, TargetTotal: 90000, ReserveOutput: 1000})
	cut, split := m.findCutPoint(msgs, 500, true) // allowSplit=true
	if !split {
		t.Fatal("expected split turn")
	}
	if cut <= 2 {
		t.Fatalf("split cut must be inside the last turn, got %d", cut)
	}
	if msgs[cut].Role == models.RoleToolResult {
		t.Fatal("split cut must not orphan a tool_result")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/contextmgr -run TestFindCutPoint -v`
Expected: FAIL(undefined: m.findCutPoint)。

- [ ] **Step 3: 实现 `pkg/contextmgr/fold.go`**

文件头(`foldOlder` 需要 context/errors/compaction;`contextmgr` import `compaction` 不构成循环,因为 `pkg/compaction` 不 import `pkg/contextmgr`):

```go
package contextmgr

import (
	"context"
	"errors"

	"github.com/lcoder/lcoder/pkg/compaction"
	"github.com/lcoder/lcoder/pkg/models"
)
```

`findCutPoint`:

// findCutPoint returns the index at which to cut msgs: [0:cut] is folded,
// [cut:] is kept. It walks backward accumulating estimated tokens until the
// budget is exhausted, then adjusts to legal boundaries:
//
//   - never cut immediately before a tool_result (its tool_use would be
//     summarized away while the result survives as an orphan); the cut is
//     advanced past the leading tool_result run so the pair stays intact.
//   - the kept tail must include the last user message. When the budget cut
//     would land inside the final turn, the cut moves back to the turn start
//     unless allowSplit (reactive pressure) permits cutting mid-turn.
//   - a message-count floor (keepRecent) protects short/small conversations
//     from over-compaction; the cut that keeps MORE wins, except under
//     allowSplit where the token budget is authoritative.
//
// split=true means the cut lands inside the last turn (split turn).
func (m *Manager) findCutPoint(msgs []models.AgentMessage, tokenBudget int, allowSplit bool) (cut int, split bool) {
	n := len(msgs)
	if n == 0 {
		return 0, false
	}

	// Walk backward accumulating tokens.
	acc := 0
	cut = 0
	for i := n - 1; i >= 0; i-- {
		t := m.EstimateTokens(msgs[i : i+1])
		if acc+t > tokenBudget {
			cut = i + 1
			break
		}
		acc += t
	}
	if cut == 0 {
		return 0, false // everything fits; nothing to fold
	}

	// Legal boundary: never cut before a tool_result.
	for cut < n && msgs[cut].Role == models.RoleToolResult {
		cut++
	}
	if cut >= n {
		cut = n - 1 // degenerate tail; fold up to the last message
	}

	// Last user protection.
	lastUserIdx := -1
	for i := n - 1; i >= 0; i-- {
		if msgs[i].Role == models.RoleUser {
			lastUserIdx = i
			break
		}
	}
	if lastUserIdx >= 0 && cut > lastUserIdx {
		if allowSplit {
			split = true
		} else {
			cut = lastUserIdx
		}
	}

	// Message-count floor (skipped when splitting: token budget is authoritative).
	if !split {
		floor := n - min(m.keepRecent, n)
		if m.keepRecent < 1 {
			floor = n - min(1, n)
		}
		if cut > floor {
			cut = floor
		}
		// The floor may re-land on a tool_result; re-adjust.
		for cut < n && msgs[cut].Role == models.RoleToolResult {
			cut++
		}
	}

	if cut <= 0 {
		return 0, false
	}
	return cut, split
}
```

- [ ] **Step 4: 修改 `pkg/contextmgr/manager.go` 与 `levels.go`,接入新切点**

`manager.go` 增加字段与 option:

```go
// keepRecentTokens is the token budget for the kept tail at proactive
// pressure; tighter levels derive from it in keepForLevel.
keepRecentTokens int
```

```go
// WithKeepRecentTokens sets the token budget for the kept tail. Zero or
// negative falls back to the default of 20000 (pi's keepRecentTokens).
func WithKeepRecentTokens(n int) Option {
	return func(m *Manager) {
		if n <= 0 {
			n = defaultKeepRecentTokens
		}
		m.keepRecentTokens = n
	}
}
```

`NewManager` 默认值:`keepRecentTokens: defaultKeepRecentTokens`。新增常量(放在 `fold.go`):

```go
// defaultKeepRecentTokens mirrors pi's keepRecentTokens default.
const defaultKeepRecentTokens = 20000
```

`levels.go` 的 `keepForLevel` 改为返回 token 预算:

```go
// keepTokensForLevel returns the kept-tail token budget for each pressure
// level: the hotter the pressure, the smaller the surviving tail. The budget
// is capped at 30% of the effective input window so the kept tail cannot
// immediately re-trigger compaction next turn.
func (m *Manager) keepTokensForLevel(level CompactionLevel) int {
	base := m.keepRecentTokens
	if base <= 0 {
		base = defaultKeepRecentTokens
	}
	var budget int
	switch level {
	case CompactionProactive:
		budget = base
	case CompactionPreflight:
		budget = base / 2
	case CompactionReactive:
		budget = base / 5
	default:
		budget = base
	}
	if eff := m.budget.EffectiveInput(); eff > 0 {
		if cap30 := eff * 30 / 100; budget > cap30 {
			budget = cap30
		}
	}
	if budget < 256 {
		budget = 256
	}
	return budget
}
```

`MaybeCompactLeveled` 改为(返回 `FoldResult`,Task 7 补全类型;本步先用):

```go
// MaybeCompactLeveled commits a multi-level compaction at a turn boundary.
func (m *Manager) MaybeCompactLeveled(ctx context.Context) (CompactionLevel, FoldResult, error) {
	if m.summarizer == nil {
		return CompactionNone, FoldResult{}, nil
	}
	recent, ok := m.GetBlock(BlockRecent, "recent")
	if !ok || len(recent.Messages) < minLeveledMessages {
		return CompactionNone, FoldResult{}, nil
	}
	level := m.budget.PressureLevel(m.currentTotalTokens())
	if level == CompactionNone {
		return CompactionNone, FoldResult{}, nil
	}
	res, err := m.foldOlder(ctx, level)
	return level, res, err
}
```

`MaybeCompact`(legacy)改为:

```go
func (m *Manager) MaybeCompact() (bool, error) {
	_, res, err := m.MaybeCompactLeveled(context.Background())
	return res.Committed, err
}
```

(manager.go import `"context"`;levels.go 同样需要。)

把旧 `foldOlder`(manager.go:398-441)整体删除,新实现放进 `fold.go`:

```go
// FoldResult describes a committed (or degraded) fold.
type FoldResult struct {
	Committed    bool
	Summary      string // committed summary text; empty when Degraded
	FirstKeptID  string // ID of the first kept message (cut boundary)
	TokensBefore int    // estimated total prompt tokens before the fold
	Degraded     bool   // true when the breaker is open: truncated without summary
	SplitTurn    bool   // true when the cut landed inside the last turn
}

// foldOlder folds messages [0:cut] of the recent block into a summary and
// commits [summary, tail...] in place. The cut point comes from findCutPoint
// with a per-level token budget. Split turns summarize history and turn
// prefix separately and merge them. A circuit-breaker-open summarizer
// (ErrCompactionSkipped) degrades to truncation without summary so context
// pressure is still relieved. State is untouched on any other error.
func (m *Manager) foldOlder(ctx context.Context, level CompactionLevel) (FoldResult, error) {
	recent, ok := m.GetBlock(BlockRecent, "recent")
	if !ok || len(recent.Messages) == 0 {
		return FoldResult{}, nil
	}
	msgs := recent.Messages
	tokensBefore := m.currentTotalTokens()

	cut, split := m.findCutPoint(msgs, m.keepTokensForLevel(level), level == CompactionReactive)
	if cut == 0 {
		return FoldResult{}, nil
	}
	res := FoldResult{
		Committed:    true,
		FirstKeptID:  msgs[cut].ID,
		TokensBefore: tokensBefore,
		SplitTurn:    split,
	}

	summaryText, err := m.summarizeForFold(ctx, msgs, cut, split)
	if err != nil {
		if errors.Is(err, compaction.ErrCompactionSkipped) {
			// Degraded: drop the older span without a summary.
			m.ReplaceRecent(append([]models.AgentMessage(nil), msgs[cut:]...))
			res.Degraded = true
			return res, nil
		}
		return FoldResult{}, err
	}

	res.Summary = "[Summary of earlier conversation]\n\n" + summaryText
	summary := models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: res.Summary}).
		WithMetadata("compacted", true)
	m.ReplaceRecent(append([]models.AgentMessage{summary}, msgs[cut:]...))
	return res, nil
}

// summarizeForFold produces the summary body for the folded span. Split turns
// summarize the pre-turn history and the in-turn prefix separately.
func (m *Manager) summarizeForFold(ctx context.Context, msgs []models.AgentMessage, cut int, split bool) (string, error) {
	if !split {
		return m.summarizer(ctx, msgs[:cut])
	}
	lastUserIdx := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == models.RoleUser {
			lastUserIdx = i
			break
		}
	}
	turnStart := lastUserIdx
	if turnStart < 0 {
		turnStart = 0
	}
	var histSummary string
	if turnStart > 0 {
		s, err := m.summarizer(ctx, msgs[:turnStart])
		if err != nil {
			return "", err
		}
		histSummary = s
	}
	prefixSummary, err := m.summarizer(ctx, msgs[turnStart:cut])
	if err != nil {
		return "", err
	}
	if histSummary == "" {
		return "[Summary of current turn so far]\n" + prefixSummary, nil
	}
	return histSummary + "\n\n[Summary of current turn so far]\n" + prefixSummary, nil
}
```

`foldOlder` 中的降级判定直接 `errors.Is(err, compaction.ErrCompactionSkipped)`,无需额外 helper。`min` 在 Go 1.21+ 是内建。

- [ ] **Step 5: 更新既有测试适配新行为**

`manager_compact_test.go` 的 `TestMaybeCompactCommitsAndFolds` / `TestMaybeCompactRollingFold` 使用 `MaxTotal: 2400, TargetTotal: 1000` 与 200 字符消息——确认仍触发 proactive 且切点合理;若断言条数变化,按新语义调整(摘要恒为一条、最后 user 在尾部这两条不变量必须保持)。

- [ ] **Step 6: 运行测试**

Run: `go test ./pkg/contextmgr -v`
Expected: 全部 PASS(新 5 个 + 既有)。

- [ ] **Step 7: Commit**

```bash
git add pkg/contextmgr
git commit -m "feat(contextmgr): token-budget cut points with split-turn summarization"
```

---

### Task 5: 熔断降级路径测试补强

Task 4 已实现降级逻辑(`ErrCompactionSkipped` → 无摘要截断)。本任务只补测试。

**Files:**
- Test: `pkg/contextmgr/fold_test.go`(追加)

- [ ] **Step 1: 写失败测试**

```go
// 熔断 OPEN(ErrCompactionSkipped)时降级为无摘要截断:状态改变、Degraded=true、
// 尾部合法(不以孤儿 tool_result 开头)、无错误返回。
func TestFoldOlderDegradesOnBreakerOpen(t *testing.T) {
	mgr := NewManager(TokenBudget{MaxTotal: 2400, TargetTotal: 1000, ReserveOutput: 200},
		WithSummarizer(func(_ context.Context, _ []models.AgentMessage) (string, error) {
			return "", compaction.ErrCompactionSkipped
		}),
		WithMinRecent(2),
		WithKeepRecentTokens(100),
	)
	var msgs []models.AgentMessage
	for i := 0; i < 10; i++ {
		msgs = append(msgs, msgAt(models.RoleUser, 200), msgAt(models.RoleAssistant, 200))
	}
	mgr.SetBlock(NewBlock(BlockRecent, "recent", StabilityDynamic, 100, msgs...))

	level, res, err := mgr.MaybeCompactLeveled(context.Background())
	if err != nil {
		t.Fatalf("degraded fold must not error, got %v", err)
	}
	if level == CompactionNone || !res.Committed || !res.Degraded {
		t.Fatalf("expected committed degraded fold, got level=%v res=%+v", level, res)
	}
	if res.Summary != "" {
		t.Fatal("degraded fold must not carry a summary")
	}
	recent, _ := mgr.GetBlock(BlockRecent, "recent")
	if len(recent.Messages) >= len(msgs) {
		t.Fatal("degraded fold must shrink the recent block")
	}
	if recent.Messages[0].Role == models.RoleToolResult {
		t.Fatal("tail must not start with orphan tool_result")
	}
	for _, m := range recent.Messages {
		if v, ok := m.Metadata["compacted"].(bool); ok && v {
			t.Fatal("degraded fold must not inject a summary message")
		}
	}
}
```

文件 import 增加 `"context"`、`"github.com/lcoder/lcoder/pkg/compaction"`。

- [ ] **Step 2: 运行测试**

Run: `go test ./pkg/contextmgr -run TestFoldOlderDegradesOnBreakerOpen -v`
Expected: PASS(Task 4 已实现;若 FAIL 则修正 fold.go)。

- [ ] **Step 3: Commit**

```bash
git add pkg/contextmgr/fold_test.go
git commit -m "test(contextmgr): cover circuit-breaker degraded fold path"
```

---

### Task 6: CompactionCommittedEvent 载荷 + loop.go 接线

**Files:**
- Modify: `pkg/events/types.go:120-123`、`pkg/agent/loop.go:504-529`
- Test: `pkg/agent/compact_test.go`(追加)

- [ ] **Step 1: 写失败测试(追加到 `pkg/agent/compact_test.go`)**

```go
// 压缩提交时事件携带 summary、firstKeptID、tokensBefore。
func TestAgentCompactionEventCarriesPayload(t *testing.T) {
	mgr := contextmgr.NewManager(
		contextmgr.TokenBudget{MaxTotal: 2000, TargetTotal: 100, ReserveOutput: 0},
		contextmgr.WithSummarizer(func(_ context.Context, _ []models.AgentMessage) (string, error) { return "s", nil }),
		contextmgr.WithMinRecent(2),
	)
	var recent []models.AgentMessage
	for i := 0; i < 20; i++ {
		recent = append(recent, models.UserMessage(strings.Repeat("u", 200)))
		recent = append(recent, models.AssistantMessage(strings.Repeat("a", 200)))
	}
	mgr.SetBlock(contextmgr.NewBlock(contextmgr.BlockRecent, "recent", contextmgr.StabilityDynamic, 100, recent...))

	a := &Agent{mgr: mgr, bus: events.New()}
	var got events.CompactionCommittedEvent
	var saw bool
	unsub := a.bus.Subscribe(func(ctx context.Context, ev events.Event) error {
		if e, ok := ev.(events.CompactionCommittedEvent); ok {
			got, saw = e, true
		}
		return nil
	})
	defer unsub()

	a.maybeCompact(context.Background(), 1)
	if !saw {
		t.Fatal("expected CompactionCommittedEvent")
	}
	if got.Summary == "" || got.FirstKeptID == "" || got.TokensBefore <= 0 {
		t.Fatalf("event payload incomplete: %+v", got)
	}
	if got.Degraded {
		t.Fatal("healthy summarizer must not degrade")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/agent -run TestAgentCompactionEventCarriesPayload -v`
Expected: FAIL(结构体无这些字段)。

- [ ] **Step 3: 修改 `pkg/events/types.go`**

```go
// CompactionCommittedEvent signals that the context manager folded older
// messages into a summary and committed the compacted window in place. The
// persistence layer reacts by appending a CompactionEntry to the session
// (append-only; raw messages are never discarded). Degraded=true means the
// circuit breaker was open and older messages were truncated without a
// summary — persistence must skip the entry in that case.
type CompactionCommittedEvent struct {
	Base
	Summary      string `json:"summary,omitempty"`
	FirstKeptID  string `json:"first_kept_entry_id,omitempty"`
	TokensBefore int    `json:"tokens_before,omitempty"`
	Degraded     bool   `json:"degraded,omitempty"`
}
```

- [ ] **Step 4: 修改 `pkg/agent/loop.go` 的 `maybeCompact`**

```go
// maybeCompact asks the context manager to commit a compaction at a turn
// boundary. On commit it emits CompactionCommitted (with the summary payload)
// so the persistence layer can append a CompactionEntry. A summarizer error
// is non-fatal; a canceled context (abort) is silent.
func (a *Agent) maybeCompact(ctx context.Context, turn int) {
	level, res, err := a.mgr.MaybeCompactLeveled(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		a.emit(ctx, events.ErrorEvent{
			Base:    events.Base{Type: events.Error, Turn: turn},
			Message: "compaction: " + err.Error(),
		})
		return
	}
	if res.Committed && a.contextSnapshotRecorder != nil {
		if state, err := a.mgr.Snapshot(); err == nil {
			_ = a.contextSnapshotRecorder.Record(state, "compaction", turn)
		}
	}
	if res.Degraded {
		a.emit(ctx, events.ErrorEvent{
			Base:    events.Base{Type: events.Error, Turn: turn},
			Message: "compaction degraded: summarizer circuit open; older messages truncated without summary",
		})
	}
	if res.Committed {
		a.emit(ctx, events.CompactionCommittedEvent{
			Base:         events.Base{Type: events.CompactionCommitted, Turn: turn},
			Summary:      res.Summary,
			FirstKeptID:  res.FirstKeptID,
			TokensBefore: res.TokensBefore,
			Degraded:     res.Degraded,
		})
		if level == contextmgr.CompactionReactive {
			if total := a.mgr.Stats()["total"]; total > a.mgr.Budget().DropLimit() {
				a.emit(ctx, events.ErrorEvent{
					Base:    events.Base{Type: events.Error, Turn: turn},
					Message: "context still over drop limit after compaction; truncation backstop active",
				})
			}
		}
	}
}
```

loop.go import 增加 `"errors"` 与 `"github.com/lcoder/lcoder/pkg/contextmgr"`(若未有,按文件既有 import 列表补)。

- [ ] **Step 5: 运行测试**

Run: `go test ./pkg/agent -v`
Expected: 全部 PASS。

- [ ] **Step 6: Commit**

```bash
git add pkg/events/types.go pkg/agent/loop.go pkg/agent/compact_test.go
git commit -m "feat(agent): carry compaction payload on CompactionCommitted and silence abort-cancel"
```

---

### Task 7: Session CompactionEntry 与 EffectiveMessages

**Files:**
- Modify: `pkg/session/store.go`
- Test: `pkg/session/compaction_test.go`(新建)

- [ ] **Step 1: 写失败测试 `pkg/session/compaction_test.go`**

```go
package session

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func newTestSession(t *testing.T, msgs ...models.AgentMessage) *Session {
	t.Helper()
	store := NewStore(t.TempDir())
	sess, err := store.Create("/tmp/proj")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, m := range msgs {
		if err := sess.Append(m); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	return sess
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			n++
		}
	}
	return n
}

// E1: 压缩条目追加后原始消息全部保留,文件行数 = 原消息数 + 1。
func TestAppendCompactionEntryKeepsRawMessages(t *testing.T) {
	msgs := []models.AgentMessage{
		models.UserMessage("one"), models.AssistantMessage("two"),
		models.UserMessage("three"), models.AssistantMessage("four"),
	}
	sess := newTestSession(t, msgs...)
	before := countLines(t, sess.Path)

	if err := sess.AppendCompactionEntry("SUMMARY TEXT", msgs[2].ID, 1234); err != nil {
		t.Fatalf("append entry: %v", err)
	}
	if got := countLines(t, sess.Path); got != before+1 {
		t.Fatalf("expected %d lines, got %d", before+1, got)
	}
	// 重新加载:四条原始消息 + 条目都在。
	store := NewStore("")
	loaded, err := store.Load(sess.Path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Messages) != len(msgs)+1 {
		t.Fatalf("expected %d messages on disk, got %d", len(msgs)+1, len(loaded.Messages))
	}
	var entry models.AgentMessage
	for _, m := range loaded.Messages {
		if IsCompactionEntry(m) {
			entry = m
		}
	}
	if entry.ID == "" {
		t.Fatal("compaction entry not found after reload")
	}
	if got, _ := entry.Metadata["first_kept_entry_id"].(string); got != msgs[2].ID {
		t.Fatalf("first_kept_entry_id mismatch: %q", got)
	}
	if tb, _ := entry.Metadata["tokens_before"].(float64); int(tb) != 1234 {
		t.Fatalf("tokens_before mismatch: %v", entry.Metadata["tokens_before"])
	}
}

// E2: 视图 = 摘要 + firstKeptEntryId 起的消息;原始旧消息不在视图中。
func TestEffectiveMessagesWithEntry(t *testing.T) {
	msgs := []models.AgentMessage{
		models.UserMessage("one"), models.AssistantMessage("two"),
		models.UserMessage("three"), models.AssistantMessage("four"),
	}
	sess := newTestSession(t, msgs...)
	if err := sess.AppendCompactionEntry("SUM", msgs[2].ID, 100); err != nil {
		t.Fatalf("append entry: %v", err)
	}
	view := sess.EffectiveMessages()
	if len(view) != 3 { // 摘要 + three + four
		t.Fatalf("expected 3 messages in view, got %d: %v", len(view), view)
	}
	if v, ok := view[0].Metadata["compacted"].(bool); !ok || !v {
		t.Fatal("view head must be a compacted summary")
	}
	if !strings.Contains(view[0].Text(), "SUM") {
		t.Fatalf("summary text missing: %q", view[0].Text())
	}
	if view[1].Text() != "three" || view[2].Text() != "four" {
		t.Fatalf("kept tail wrong: %q %q", view[1].Text(), view[2].Text())
	}
}

// E2b: 多次压缩——只用最新条目;第二次 firstKept 指向 entry 之后的消息。
func TestEffectiveMessagesMultipleEntries(t *testing.T) {
	msgs := []models.AgentMessage{
		models.UserMessage("one"), models.AssistantMessage("two"),
	}
	sess := newTestSession(t, msgs...)
	_ = sess.AppendCompactionEntry("SUM1", msgs[0].ID, 100)
	m3, m4 := models.UserMessage("three"), models.AssistantMessage("four")
	_ = sess.Append(m3)
	_ = sess.Append(m4)
	_ = sess.AppendCompactionEntry("SUM2", m3.ID, 200)

	view := sess.EffectiveMessages()
	if len(view) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(view))
	}
	if !strings.Contains(view[0].Text(), "SUM2") {
		t.Fatalf("must use newest entry, got %q", view[0].Text())
	}
	if view[1].Text() != "three" {
		t.Fatalf("kept must start at SUM2's firstKept, got %q", view[1].Text())
	}
}

// E3: 悬挂 firstKeptEntryId → 回退到条目之后的所有消息。
func TestEffectiveMessagesDanglingFirstKept(t *testing.T) {
	msgs := []models.AgentMessage{models.UserMessage("one"), models.AssistantMessage("two")}
	sess := newTestSession(t, msgs...)
	_ = sess.AppendCompactionEntry("SUM", "nonexistent-id", 100)
	m3 := models.UserMessage("three")
	_ = sess.Append(m3)
	view := sess.EffectiveMessages()
	if len(view) != 2 || view[1].Text() != "three" {
		t.Fatalf("dangling firstKept must fall back to post-entry messages: %v", view)
	}
}

// E4: 分支场景——在 main 压缩后,fork 出的分支消息不受影响。
func TestEffectiveMessagesBranchCoexistence(t *testing.T) {
	msgs := []models.AgentMessage{models.UserMessage("one"), models.AssistantMessage("two")}
	sess := newTestSession(t, msgs...)
	branchID, err := sess.Fork(msgs[0].ID)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	bm := models.UserMessage("branch msg")
	_ = sess.Append(bm)
	if err := sess.SwitchBranch(mainBranch); err != nil {
		t.Fatalf("switch: %v", err)
	}
	_ = sess.AppendCompactionEntry("SUM", msgs[1].ID, 100)

	// main 视图正常。
	if view := sess.EffectiveMessages(); len(view) != 2 {
		t.Fatalf("main view: expected 2, got %d", len(view))
	}
	// 切回分支:分支消息仍在,且无 compaction entry 在分支链上。
	if err := sess.SwitchBranch(branchID); err != nil {
		t.Fatalf("switch back: %v", err)
	}
	view := sess.EffectiveMessages()
	var texts []string
	for _, m := range view {
		texts = append(texts, m.Text())
	}
	joined := strings.Join(texts, "|")
	if !strings.Contains(joined, "branch msg") {
		t.Fatalf("branch message lost: %v", texts)
	}
}

// E5: 无条目的旧 session 与旧 Replace 格式(含 compacted 摘要消息)行为不变。
func TestEffectiveMessagesLegacySessions(t *testing.T) {
	msgs := []models.AgentMessage{models.UserMessage("one"), models.AssistantMessage("two")}
	sess := newTestSession(t, msgs...)
	if view := sess.EffectiveMessages(); len(view) != 2 {
		t.Fatalf("legacy linear session must pass through, got %d", len(view))
	}

	summary := models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: "[Summary of earlier conversation]\n\nold"}).
		WithMetadata("compacted", true)
	sess2 := newTestSession(t, summary, models.UserMessage("after"))
	if view := sess2.EffectiveMessages(); len(view) != 2 {
		t.Fatalf("legacy replaced session must pass through, got %d", len(view))
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/session -run 'TestAppendCompactionEntry|TestEffectiveMessages' -v`
Expected: FAIL(undefined: IsCompactionEntry / AppendCompactionEntry / EffectiveMessages)。

- [ ] **Step 3: 实现 `pkg/session/store.go` 新增部分**

常量与判定:

```go
// Metadata keys for compaction entries.
const (
	MetaType               = "type"
	MetaTypeCompaction     = "compaction"
	MetaFirstKeptEntryID   = "first_kept_entry_id"
	MetaTokensBefore       = "tokens_before"
)

// IsCompactionEntry reports whether m is an append-only compaction entry.
func IsCompactionEntry(m models.AgentMessage) bool {
	if m.Metadata == nil {
		return false
	}
	v, ok := m.Metadata[MetaType].(string)
	return ok && v == MetaTypeCompaction
}
```

`AppendCompactionEntry`(复用 Append 的元数据/parent 逻辑,写盘用追加单行):

```go
// AppendCompactionEntry appends a compaction entry to the session without
// rewriting existing lines: raw history is never discarded. The entry carries
// the summary text and the id of the first kept message; EffectiveMessages
// uses it to rebuild the compacted view. Parent is the current branch head,
// so the branch chain stays continuous through the entry.
func (s *Session) AppendCompactionEntry(summary, firstKeptEntryID string, tokensBefore int) error {
	msg := models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: summary})
	msg.Metadata[MetaType] = MetaTypeCompaction
	msg.Metadata[MetaFirstKeptEntryID] = firstKeptEntryID
	msg.Metadata[MetaTokensBefore] = tokensBefore

	if err := s.stage(msg); err != nil {
		return err
	}
	return s.appendLine(s.Messages[len(s.Messages)-1])
}

// stage applies the common metadata/parent wiring and appends the message to
// the in-memory list (shared by Append and AppendCompactionEntry).
func (s *Session) stage(msg models.AgentMessage) error {
	if msg.Metadata == nil {
		msg.Metadata = make(map[string]any)
	}
	msg.Metadata["session_id"] = s.ID
	msg.Metadata["cwd"] = s.CWD
	msg.Metadata["saved_at"] = time.Now().UnixMilli()
	msg.Metadata["branch_id"] = s.activeBranch

	if msg.ID == "" {
		msg.ID = uuid.New().String()[:12]
	}
	if msg.ParentID == nil || *msg.ParentID == "" {
		if head, ok := s.branchHeads[s.activeBranch]; ok && head != "" {
			msg.ParentID = &head
		}
	}

	s.Messages = append(s.Messages, msg)
	s.branchHeads[s.activeBranch] = msg.ID
	return nil
}

// appendLine persists exactly one message by appending it to the session file,
// preserving every existing byte. Falls back to a full atomic Save when the
// file does not exist yet.
func (s *Session) appendLine(msg models.AgentMessage) error {
	if err := fsutil.EnsurePrivateDir(filepath.Dir(s.Path)); err != nil {
		return err
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(s.Path, 0o600)
}
```

`Append` 改为复用 stage:

```go
// Append adds a message to the current branch and persists it.
func (s *Session) Append(msg models.AgentMessage) error {
	if err := s.stage(msg); err != nil {
		return err
	}
	return s.Save()
}
```

`EffectiveMessages`:

```go
// EffectiveMessages returns the compacted view of the active branch: the
// newest compaction entry's summary plus the branch messages from its
// first_kept_entry_id onwards (falling back to all post-entry messages when
// that id is not on the branch). Without any compaction entry it is identical
// to ActiveMessages. Raw messages always remain on disk; this is only the
// view fed to the runtime context.
func (s *Session) EffectiveMessages() []models.AgentMessage {
	active := s.ActiveMessages()
	entryIdx := -1
	for i := len(active) - 1; i >= 0; i-- {
		if IsCompactionEntry(active[i]) {
			entryIdx = i
			break
		}
	}
	if entryIdx < 0 {
		return active
	}

	entry := active[entryIdx]
	after := active[entryIdx+1:]
	kept := after
	if firstKept, _ := entry.Metadata[MetaFirstKeptEntryID].(string); firstKept != "" {
		for i, m := range after {
			if m.ID == firstKept {
				kept = after[i:]
				break
			}
		}
	}

	summary := entry
	summary.Metadata = make(map[string]any, len(entry.Metadata)+1)
	for k, v := range entry.Metadata {
		summary.Metadata[k] = v
	}
	delete(summary.Metadata, MetaType)
	summary.Metadata["compacted"] = true

	out := make([]models.AgentMessage, 0, len(kept)+1)
	out = append(out, summary)
	out = append(out, kept...)
	return out
}
```

`Replace` 标注 deprecated:

```go
// Replace overwrites the session's entire conversation with msgs and persists
// it.
//
// Deprecated: compaction persistence now uses AppendCompactionEntry, which is
// append-only and never discards raw messages. Replace is kept for tests.
```

- [ ] **Step 4: 运行测试**

Run: `go test ./pkg/session -v`
Expected: 全部 PASS(新 6 个 + 既有 branch/replace/append 测试)。

- [ ] **Step 5: Commit**

```bash
git add pkg/session/store.go pkg/session/compaction_test.go
git commit -m "feat(session): append-only CompactionEntry and EffectiveMessages view"
```

---

### Task 8: main.go 持久化切换 + 启动加载 EffectiveMessages

**Files:**
- Modify: `cmd/lcoder/main.go:290`、`:367`、`:545-556`、`:626-634`

- [ ] **Step 1: 修改两处持久化订阅(runOneShot 与 runTUI 内,改动相同)**

```go
	persistHandler := func(ctx context.Context, ev events.Event) error {
		switch e := ev.(type) {
		case events.CompactionCommittedEvent:
			// Append-only: record the compaction entry; raw messages stay on disk.
			// Degraded folds (breaker open) carry no summary and persist nothing.
			if !e.Degraded && e.Summary != "" {
				_ = setup.sess.AppendCompactionEntry(e.Summary, e.FirstKeptID, e.TokensBefore)
			}
		case events.MessageEndEvent, events.ToolExecutionEndEvent, events.AgentEndEvent:
			_ = setup.sess.Save()
		}
		return nil
	}
```

- [ ] **Step 2: 修改启动加载两处**

`main.go:290`:

```go
	mgr := agentsetup.NewContextManager(cfg, budget, llmClient, contextText, skillsBlock, sess.EffectiveMessages(), memStore)
```

`main.go:367`:

```go
	ag.SetMessages(sess.EffectiveMessages())
```

- [ ] **Step 3: 编译 + 相关测试**

Run: `go build ./... && go test ./cmd/... ./pkg/session ./pkg/agent`
Expected: 通过。再跑手动 smoke(可选):

```bash
go build -o lcoder ./cmd/lcoder
# 用一个超大会话触发压缩后,检查 session 文件行数只增不减、含 "type":"compaction" 行
```

- [ ] **Step 4: Commit**

```bash
git add cmd/lcoder/main.go
git commit -m "feat(cli): persist compaction as append-only entry; load EffectiveMessages view"
```

---

### Task 9: 配置 keep_recent_tokens 三场景对齐

**Files:**
- Modify: `pkg/config/config.go:79-101`、`:131-141`、`:360-370` 附近、`pkg/config/config_validate.go:98-100`、`pkg/agentsetup/setup.go:52-57`
- Modify: `configs/lcoder.yaml`、`eval/swe-bench-lite/config/lcoder.yaml:21-26`
- Test: `pkg/config/config_test.go`(追加)

- [ ] **Step 1: 写失败测试(追加到 `pkg/config/config_test.go`)**

```go
func TestContextConfigKeepRecentTokens(t *testing.T) {
	// 默认值。
	cfg := Default()
	if cfg.Context.KeepRecentTokens != 20000 {
		t.Fatalf("default keep_recent_tokens = %d, want 20000", cfg.Context.KeepRecentTokens)
	}
	// 校验:负数拒绝。
	bad := Default()
	bad.Context.KeepRecentTokens = -1
	if err := bad.Context.Validate(); err == nil {
		t.Fatal("negative keep_recent_tokens must be rejected")
	}
}
```

`Default()`/`Validate()` 的确切名字以 `config_test.go` 既有用法为准(文件内已有 `TestResolveContextBudgetDropThreshold` 等用例直接构造 `Config{Context: ContextConfig{...}}`,可同样直接构造)。

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/config -run TestContextConfigKeepRecentTokens -v`
Expected: FAIL(无 KeepRecentTokens 字段)。

- [ ] **Step 3: 修改 `pkg/config/config.go`**

`ContextConfig` 增加字段(放在 `MinRecent` 后):

```go
	MinRecent        int      `yaml:"min_recent"`         // minimum recent messages to keep
	KeepRecentTokens int      `yaml:"keep_recent_tokens"` // token budget for the kept tail at proactive pressure (0 = default 20000)
```

`Default()` 的 Context 初始化增加:

```go
		MinRecent:        10,
		KeepRecentTokens: 20000,
```

env override 映射(`:360-370` 附近)增加:

```go
		"keep_recent_tokens": cfg.Context.KeepRecentTokens,
```

`config_validate.go` 的 `ContextConfig.Validate` 增加:

```go
	if c.KeepRecentTokens < 0 {
		return fmt.Errorf("keep_recent_tokens must be non-negative")
	}
```

- [ ] **Step 4: 修改 `pkg/agentsetup/setup.go`**

opts 列表增加一行:

```go
		contextmgr.WithKeepRecentTokens(cfg.Context.KeepRecentTokens),
```

- [ ] **Step 5: 修改两个 yaml**

`configs/lcoder.yaml` 的 `context:` 段增加(放在 min_recent 旁):

```yaml
  min_recent: 10
  keep_recent_tokens: 20000  # 压缩时保留尾部的 token 预算(proactive 档);preflight 减半、reactive 取 1/5
```

`eval/swe-bench-lite/config/lcoder.yaml:26` 后同样增加:

```yaml
  min_recent: 10
  keep_recent_tokens: 20000
```

- [ ] **Step 6: 运行测试**

Run: `go test ./pkg/config ./pkg/agentsetup -v`
Expected: PASS。

- [ ] **Step 7: Commit**

```bash
git add pkg/config pkg/agentsetup configs/lcoder.yaml eval/swe-bench-lite/config/lcoder.yaml
git commit -m "feat(config): add keep_recent_tokens aligned across prod and eval configs"
```

---

### Task 10: 集成 round-trip 测试 + 全量回归

**Files:**
- Test: `test/integration/compaction_test.go`(追加子用例)

- [ ] **Step 1: 写 round-trip 子用例**

追加到 `TestCompactionMechanisms` 内(作为第 7 个子用例,复用文件顶部的 `convo`/`renderMsgs`/`mechanismReport`):

```go
	// 7. Append-only persistence round-trip: 压缩后磁盘保留全部原始消息 + 条目,
	//    重载后 EffectiveMessages 重建压缩视图。
	t.Run("AppendOnlyPersistence_RoundTrip", func(t *testing.T) {
		store := session.NewStore(t.TempDir())
		sess, err := store.Create("/tmp/proj")
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		before := convo(20)
		for _, m := range before {
			if err := sess.Append(m); err != nil {
				t.Fatalf("append: %v", err)
			}
		}
		linesBefore := countFileLines(t, sess.Path)

		firstKept := before[14].ID
		if err := sess.AppendCompactionEntry("[Summary of earlier conversation]\n\nround-trip summary", firstKept, 999); err != nil {
			t.Fatalf("append entry: %v", err)
		}
		if got := countFileLines(t, sess.Path); got != linesBefore+1 {
			t.Fatalf("file must grow by exactly 1 line, got %d (was %d)", got, linesBefore)
		}

		// 模拟崩溃重启:重新加载,视图必须 = 摘要 + before[14:]。
		loaded, err := store.Load(sess.Path)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		view := loaded.EffectiveMessages()
		if len(view) != 1+(len(before)-14) {
			t.Fatalf("view length = %d, want %d", len(view), 1+(len(before)-14))
		}
		if v, ok := view[0].Metadata["compacted"].(bool); !ok || !v {
			t.Fatal("view head must be compacted summary")
		}
		if view[1].ID != firstKept {
			t.Fatalf("view must resume at firstKept, got %q", view[1].ID)
		}
		// 磁盘原始消息一条不少。
		if len(loaded.Messages) != len(before)+1 {
			t.Fatalf("disk messages = %d, want %d", len(loaded.Messages), len(before)+1)
		}

		reports = append(reports, mechanismReport{
			Name:         "Append-only 持久化 round-trip",
			Detail:       "`Session.AppendCompactionEntry` 追加条目(原始消息不删);重载后 `EffectiveMessages` 重建 摘要+kept 视图。",
			Before:       renderMsgs(before),
			After:        renderMsgs(view),
			BeforeTokens: contextmgr.DefaultEstimator(before),
			AfterTokens:  contextmgr.DefaultEstimator(view),
			Verdict:      "PASS —— 磁盘 21 行(20 原始 + 1 条目),视图 = 1 摘要 + 6 kept",
		})
	})
```

文件 import 增加 `"github.com/lcoder/lcoder/pkg/session"`,并加 helper:

```go
func countFileLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}
```

- [ ] **Step 2: 运行集成测试**

Run: `go test -tags integration ./test/integration -run TestCompactionMechanisms -v`
Expected: PASS,output 目录生成含 7 个子用例的 markdown。

- [ ] **Step 3: 全量回归**

Run:

```bash
go build ./...
go vet $(go list ./... | grep -v 'reference/Shannon')
go test $(go list ./... | grep -v 'reference/Shannon') -count=1 -race
go test -tags integration $(go list ./test/integration/... ) -count=1
```

Expected: 全部通过,无 race。

- [ ] **Step 4: 更新 CLAUDE.md 架构描述**

`CLAUDE.md` 的 "Context management" 与 "Session storage" 段落更新为:压缩按 token 预算切点(proactive/preflight/reactive 三档),split turn 双摘要;session 为 append-only,压缩以 CompactionEntry 追加,`EffectiveMessages` 重建视图。

- [ ] **Step 5: Commit**

```bash
git add test/integration/compaction_test.go CLAUDE.md
git commit -m "test(integration): append-only persistence round-trip; docs: update architecture notes"
```

---

## Self-Review 记录

- Spec 覆盖:A→Task 4;B→Task 4(findCutPoint split)+ Task 4 summarizeForFold;C→Task 2/3;D→Task 1 + Task 6(cancel 静默);E→Task 7/8;配置对齐→Task 9;测试矩阵→各任务 Step 1 + Task 10。
- 明确不做(分支摘要、/compact、文件跟踪、GC)无对应任务,符合 spec。
- 类型一致性:`FoldResult`、`CompactionCommittedEvent{Summary, FirstKeptID, TokensBefore, Degraded}`、`AppendCompactionEntry(summary, firstKeptEntryID string, tokensBefore int)`、`EffectiveMessages()` 跨任务一致;`SummarizeFunc func(ctx, msgs) (string, error)` 两包一致。
- `Replace` 保留给既有 `replace_test.go` 使用,标 deprecated 不删除。
