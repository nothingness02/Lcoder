package contextmgr

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/compaction"
	"github.com/lcoder/lcoder/pkg/models"
)

// msgAt 构造一条指定角色、指定字符数的文本消息(4 字符 ≈ 1 token)。
func msgAt(role models.MessageRole, chars int) models.AgentMessage {
	return models.NewAgentMessage(role, models.TextContent{Text: strings.Repeat("x", chars)})
}

// toolPair 构造一对 tool_use / tool_result。DefaultEstimator 只统计顶层
// TextContent,因此 tool 消息本身贡献 0 token —— 它们用于验证切点边界
// (不得在 tool_result 之前切),而非驱动 token 预算。
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
	// WithMinRecent(1) 让 token 预算成为主导约束(默认 keepRecent=10 会保 10 条)。
	m := NewManager(TokenBudget{MaxTotal: 100000, TargetTotal: 90000, ReserveOutput: 1000}, WithMinRecent(1))
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
	// 文本消息驱动 token 预算;中间插入一对 0-token 的 tool_use/tool_result,
	// 使预算切点恰好可能落在 tool_result 之前。
	msgs := []models.AgentMessage{msgAt(models.RoleUser, 400)}
	msgs = append(msgs, toolPair("c1", 400)...)
	msgs = append(msgs, msgAt(models.RoleUser, 400), msgAt(models.RoleAssistant, 400))
	m := NewManager(TokenBudget{MaxTotal: 100000, TargetTotal: 90000, ReserveOutput: 1000})
	cut, _ := m.findCutPoint(msgs, 500, false)
	if msgs[cut].Role == models.RoleToolResult {
		t.Fatalf("cut before tool_result would orphan it")
	}
	// 保留尾部不得以孤儿 tool_result 开头。
	tail := msgs[cut:]
	if len(tail) > 0 && tail[0].Role == models.RoleToolResult {
		t.Fatal("tail starts with orphan tool_result")
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
	msgs = append(msgs, msgAt(models.RoleUser, 400)) // 最后一轮开始 idx=2
	// 最后一轮自身用大文本撑大,使预算切点想切进最后一轮。
	msgs = append(msgs, msgAt(models.RoleAssistant, 40000))
	// WithMinRecent(1) 避免条数下限(默认 10 > n=4)把切点压回 0。
	m := NewManager(TokenBudget{MaxTotal: 100000, TargetTotal: 90000, ReserveOutput: 1000}, WithMinRecent(1))
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
	// 最后一轮内部:大文本 assistant + 一对 tool_use/tool_result + 结尾 assistant。
	msgs = append(msgs, msgAt(models.RoleAssistant, 40000))
	msgs = append(msgs, toolPair("c1", 400)...)
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

// 回归:floor 重新对齐后 cut 可能走到 n(尾部全是 tool_result 且其后无 user),
// findCutPoint 必须返回 (0,false) 而不是让 foldOlder 在 msgs[cut] 处越界。
func TestFindCutPointAllToolResultTail(t *testing.T) {
	// 无 user 消息:一条大文本 assistant 驱动预算,尾部是一串 0-token 的
	// tool_result。floor 重对齐会一路推进到 n。
	var msgs []models.AgentMessage
	msgs = append(msgs, msgAt(models.RoleAssistant, 2000)) // ~500 token,驱动预算压力
	for i := 0; i < 4; i++ {
		msgs = append(msgs, models.NewAgentMessage(models.RoleToolResult, models.ToolResultContent{
			ToolCallID: "c", Name: "read",
			Content: []models.ContentPart{models.TextContent{Text: strings.Repeat("r", 400)}},
		}))
	}
	m := NewManager(TokenBudget{MaxTotal: 100000, TargetTotal: 90000, ReserveOutput: 1000}, WithMinRecent(1))
	cut, split := m.findCutPoint(msgs, 100, false)
	if cut != 0 || split {
		t.Fatalf("expected no fold (cut=0,split=false), got cut=%d split=%v", cut, split)
	}

	// 端到端:经过 foldOlder 不得 panic,且不得 commit。
	m2 := NewManager(TokenBudget{MaxTotal: 100000, TargetTotal: 90000, ReserveOutput: 1000},
		WithMinRecent(1), WithSummarizer(stubSummarizer))
	m2.SetMessages(msgs)
	res, err := m2.foldOlder(context.Background(), CompactionReactive)
	if err != nil {
		t.Fatalf("foldOlder: %v", err)
	}
	if res.Committed {
		t.Fatal("expected no commit when the kept tail would be empty")
	}
}

// 降级路径:breaker 开路(ErrCompactionSkipped)时,foldOlder 截断但不注入摘要。
func TestFoldOlderDegradesOnBreakerOpen(t *testing.T) {
	budget := TokenBudget{MaxTotal: 400, ReserveOutput: 0} // EffectiveInput=400
	m := NewManager(budget,
		WithMinRecent(1),
		WithSummarizer(SummarizeFunc(func(_ context.Context, _ []models.AgentMessage, _ string) (string, error) {
			return "", compaction.ErrCompactionSkipped
		})))
	m.SetSystemPrompt("sys")
	// 20 条 × 50 token ≈ 1000 token → reactive;非零 cut。
	m.ReplaceRecent(convoMsgs(20))

	level, res, err := m.MaybeCompactLeveled(context.Background())
	if err != nil {
		t.Fatalf("MaybeCompactLeveled: %v", err)
	}
	if level == CompactionNone {
		t.Fatal("expected pressure to trigger compaction")
	}
	if !res.Committed {
		t.Fatal("expected degraded fold to still commit (truncate)")
	}
	if !res.Degraded {
		t.Fatal("expected Degraded=true on breaker-open")
	}
	// 降级必须给出显式说明,而不是空摘要:空摘要会作为一条空 system 消息到达模型,
	// 结构上宣称"此处有摘要"却不含任何信息,模型无法得知历史被截断。
	if res.Summary == "" {
		t.Fatal("degraded path must commit an explicit notice, not an empty summary")
	}
	if !strings.Contains(res.Summary, "Summary unavailable") {
		t.Fatalf("degraded summary must say the summary is unavailable, got %q", res.Summary)
	}

	recent, ok := m.GetBlock(BlockRecent, "recent")
	if !ok {
		t.Fatal("recent block missing")
	}
	if len(recent.Messages) == 0 {
		t.Fatal("degraded fold must keep a non-empty tail")
	}
	if len(recent.Messages) >= 20 {
		t.Fatalf("degraded fold must shrink the recent block, kept %d", len(recent.Messages))
	}

	// 头部必须是那条降级说明,且带 compacted 标记 —— SetMessages 靠它把摘要留在
	// recent 块而不是当成真 system 消息覆盖系统提示。
	head := recent.Messages[0]
	if head.Role != models.RoleSystem {
		t.Fatalf("degraded fold must lead with the notice, got role %q", head.Role)
	}
	if v, _ := head.Metadata["compacted"].(bool); !v {
		t.Fatal("degraded notice must carry compacted=true")
	}
	if !strings.Contains(head.Text(), "Summary unavailable") {
		t.Fatalf("head must be the degraded notice, got %q", head.Text())
	}
	// 说明之后紧跟保留尾部,不得出现孤儿 tool_result。
	if len(recent.Messages) > 1 && recent.Messages[1].Role == models.RoleToolResult {
		t.Fatal("degraded fold must not leave an orphan tool_result after the notice")
	}
}

// split turn:历史与当前轮前缀分别摘要并合并,摘要器恰好被调用两次。
func TestSummarizeForFoldSplitTurn(t *testing.T) {
	var calls [][]models.AgentMessage
	summarizer := SummarizeFunc(func(_ context.Context, msgs []models.AgentMessage, _ string) (string, error) {
		calls = append(calls, msgs)
		if len(calls) == 1 {
			return "HIST", nil
		}
		return "PREFIX", nil
	})
	// 大窗口使 30% cap 不约束;reactive 预算 = keepRecentTokens/5。
	// keepRecentTokens=1280 → reactive 预算 256(1280/5=256)。
	m := NewManager(TokenBudget{MaxTotal: 1_000_000, ReserveOutput: 0},
		WithKeepRecentTokens(1280), WithMinRecent(1), WithSummarizer(summarizer))

	// 历史轮 + 最后一轮(自身 token 超过 reactive 预算 → 切进轮内)。
	var msgs []models.AgentMessage
	msgs = append(msgs, msgAt(models.RoleUser, 400), msgAt(models.RoleAssistant, 400)) // 历史轮
	msgs = append(msgs, msgAt(models.RoleUser, 400))                                     // 最后一轮起点 idx=2
	msgs = append(msgs, msgAt(models.RoleAssistant, 40000))                              // 最后一轮内大文本
	msgs = append(msgs, msgAt(models.RoleAssistant, 400), msgAt(models.RoleAssistant, 400))
	m.ReplaceRecent(msgs)

	res, err := m.foldOlder(context.Background(), CompactionReactive)
	if err != nil {
		t.Fatalf("foldOlder: %v", err)
	}
	if !res.SplitTurn {
		t.Fatal("expected split turn")
	}
	if len(calls) != 2 {
		t.Fatalf("expected summarizer called twice (history + prefix), got %d", len(calls))
	}
	if !strings.Contains(res.Summary, "HIST") {
		t.Fatalf("committed summary must contain history summary, got %q", res.Summary)
	}
	if !strings.Contains(res.Summary, "[Summary of current turn so far]") {
		t.Fatalf("committed summary must contain the turn marker, got %q", res.Summary)
	}
	if !strings.Contains(res.Summary, "PREFIX") {
		t.Fatalf("committed summary must contain prefix summary, got %q", res.Summary)
	}
	if !strings.HasPrefix(res.Summary, "[Summary of earlier conversation]\n\n") {
		t.Fatalf("committed summary must carry the foldOlder prefix, got %q", res.Summary)
	}
}

// D: 取消传播 —— 摘要器在 ctx 取消后返回 ctx.Err(),foldOlder 必须原样上抛,
// 不得 commit,recent 块保持调用前逐字节不变。
func TestFoldOlderHonorsContextCancellation(t *testing.T) {
	budget := TokenBudget{MaxTotal: 400, ReserveOutput: 0} // EffectiveInput=400
	m := NewManager(budget,
		WithMinRecent(1),
		WithSummarizer(SummarizeFunc(func(ctx context.Context, _ []models.AgentMessage, _ string) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		})))
	m.SetSystemPrompt("sys")
	// 20 条 × 50 token ≈ 1000 token → 压力触发压缩,非零 cut。
	m.ReplaceRecent(convoMsgs(20))

	// 快照调用前的 recent 块(消息 ID 顺序)。
	before, ok := m.GetBlock(BlockRecent, "recent")
	if !ok {
		t.Fatal("recent block missing before call")
	}
	beforeIDs := make([]string, len(before.Messages))
	for i, msg := range before.Messages {
		beforeIDs[i] = msg.ID
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 调用前取消:摘要器立即返回 ctx.Err()

	_, res, err := m.MaybeCompactLeveled(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if res.Committed {
		t.Fatal("expected no commit on cancellation")
	}

	after, ok := m.GetBlock(BlockRecent, "recent")
	if !ok {
		t.Fatal("recent block missing after call")
	}
	if len(after.Messages) != len(beforeIDs) {
		t.Fatalf("recent block changed on cancellation: %d msgs, want %d", len(after.Messages), len(beforeIDs))
	}
	for i, msg := range after.Messages {
		if msg.ID != beforeIDs[i] {
			t.Fatalf("recent block msg %d changed on cancellation: id %q, want %q", i, msg.ID, beforeIDs[i])
		}
	}
}

// The sink exists so that "the context was folded" and "the fold was recorded"
// cannot diverge. Before it, persistence lived in two event subscribers in other
// packages, so this contract could not be tested at all — and a change to the
// degraded-fold semantics silently invalidated both of them while every package
// still passed.
func TestFoldCallsSinkOnCommit(t *testing.T) {
	var got []FoldResult
	var liveAtCall [][]models.AgentMessage
	m := NewManager(TokenBudget{MaxTotal: 400, ReserveOutput: 0},
		WithMinRecent(1),
		WithSummarizer(SummarizeFunc(func(_ context.Context, _ []models.AgentMessage, _ string) (string, error) {
			return "SUMMARY", nil
		})),
		WithCompactionSink(func(res FoldResult, live []models.AgentMessage) error {
			got = append(got, res)
			liveAtCall = append(liveAtCall, append([]models.AgentMessage(nil), live...))
			return nil
		}),
	)
	m.SetSystemPrompt("sys")
	m.ReplaceRecent(convoMsgs(20))

	if _, _, err := m.MaybeCompactLeveled(context.Background()); err != nil {
		t.Fatalf("MaybeCompactLeveled: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 sink call, got %d", len(got))
	}
	if !strings.Contains(got[0].Summary, "SUMMARY") {
		t.Errorf("sink got summary %q", got[0].Summary)
	}
	if got[0].FirstKeptID == "" {
		t.Error("sink must receive the cut boundary id: the compacted view is rebuilt from it")
	}
	// The sink runs after the fold is committed, so what it sees must already be
	// the smaller context — that is what it has to record.
	if len(liveAtCall[0]) >= 20 {
		t.Errorf("sink saw %d messages: it must observe the post-fold context", len(liveAtCall[0]))
	}
}

// A degraded fold really did drop the older span, so it must be recorded too.
// Skipping it would leave the session's compacted view claiming those messages
// are active, and a resume would replay them.
func TestFoldCallsSinkOnDegraded(t *testing.T) {
	var got []FoldResult
	m := NewManager(TokenBudget{MaxTotal: 400, ReserveOutput: 0},
		WithMinRecent(1),
		WithSummarizer(SummarizeFunc(func(_ context.Context, _ []models.AgentMessage, _ string) (string, error) {
			return "", compaction.ErrCompactionSkipped
		})),
		WithCompactionSink(func(res FoldResult, _ []models.AgentMessage) error {
			got = append(got, res)
			return nil
		}),
	)
	m.SetSystemPrompt("sys")
	m.ReplaceRecent(convoMsgs(20))

	if _, _, err := m.MaybeCompactLeveled(context.Background()); err != nil {
		t.Fatalf("MaybeCompactLeveled: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("degraded fold must still reach the sink, got %d calls", len(got))
	}
	if !got[0].Degraded {
		t.Error("sink must see Degraded=true")
	}
	if got[0].Summary == "" {
		t.Error("degraded fold must hand the sink its explicit notice, not an empty summary")
	}
}

// A sink failure is surfaced to the caller but must not roll the fold back: the
// context is already smaller, and re-inflating it risks overflowing the window
// the fold was relieving.
func TestFoldReportsSinkErrorWithoutRollback(t *testing.T) {
	m := NewManager(TokenBudget{MaxTotal: 400, ReserveOutput: 0},
		WithMinRecent(1),
		WithSummarizer(SummarizeFunc(func(_ context.Context, _ []models.AgentMessage, _ string) (string, error) {
			return "SUMMARY", nil
		})),
		WithCompactionSink(func(FoldResult, []models.AgentMessage) error {
			return errors.New("disk full")
		}),
	)
	m.SetSystemPrompt("sys")
	m.ReplaceRecent(convoMsgs(20))

	_, _, err := m.MaybeCompactLeveled(context.Background())
	if err == nil {
		t.Fatal("a sink failure must be reported, not swallowed")
	}
	if !strings.Contains(err.Error(), "record compaction") {
		t.Errorf("error should name the failing step, got %v", err)
	}
	recent, _ := m.GetBlock(BlockRecent, "recent")
	if len(recent.Messages) >= 20 {
		t.Error("the fold must stand despite the sink failure")
	}
}

// No sink configured is a supported configuration, not a crash.
func TestFoldWithoutSink(t *testing.T) {
	m := NewManager(TokenBudget{MaxTotal: 400, ReserveOutput: 0},
		WithMinRecent(1),
		WithSummarizer(SummarizeFunc(func(_ context.Context, _ []models.AgentMessage, _ string) (string, error) {
			return "SUMMARY", nil
		})),
	)
	m.SetSystemPrompt("sys")
	m.ReplaceRecent(convoMsgs(20))

	if _, _, err := m.MaybeCompactLeveled(context.Background()); err != nil {
		t.Fatalf("fold without a sink must succeed: %v", err)
	}
}
