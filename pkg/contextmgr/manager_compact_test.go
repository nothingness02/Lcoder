package contextmgr

import (
	"context"
	"fmt"
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
	mgr := NewManager(TokenBudget{MaxTotal: 2400, TargetTotal: 1000, ReserveOutput: 200},
		WithSummarizer(func(_ context.Context, msgs []models.AgentMessage, _ string) (string, error) {
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

// 第二次压缩必须把上一条摘要作为 prior 传入,而不是混在 msgs 里当普通消息重新
// 摘要一遍 —— 摘要的摘要每轮都在丢信息,最先丢的就是最初的任务陈述。摘要恒为一条。
func TestMaybeCompactRollingFold(t *testing.T) {
	calls := 0
	var secondPrior string
	var secondSawSummaryInMsgs bool
	mgr := NewManager(TokenBudget{MaxTotal: 2400, TargetTotal: 1000, ReserveOutput: 200},
		WithSummarizer(func(_ context.Context, msgs []models.AgentMessage, prior string) (string, error) {
			calls++
			if calls == 2 {
				secondPrior = prior
				for _, m := range msgs {
					if v, ok := m.Metadata["compacted"].(bool); ok && v {
						secondSawSummaryInMsgs = true
					}
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

	if secondPrior == "" {
		t.Error("second compaction must receive the prior summary via prior")
	}
	if strings.Contains(secondPrior, summaryDisplayPrefix) {
		t.Errorf("prior must be stripped of its display prefix, got %q", secondPrior)
	}
	if secondSawSummaryInMsgs {
		t.Error("prior summary must not also appear in msgs: it would be summarized again")
	}

	// 摘要恒为一条。
	after, _ := mgr.GetBlock(BlockRecent, "recent")
	count := 0
	for _, m := range after.Messages {
		if v, ok := m.Metadata["compacted"].(bool); ok && v {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one summary after rolling fold, got %d", count)
	}
}

// 未超阈值或无 summarizer 时不动。
func TestMaybeCompactNoopBelowThreshold(t *testing.T) {
	mgr := NewManager(TokenBudget{MaxTotal: 100000, TargetTotal: 100000, ReserveOutput: 200},
		WithSummarizer(func(_ context.Context, msgs []models.AgentMessage, _ string) (string, error) { return "x", nil }),
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

// countSummaries returns how many compaction summaries are live in the recent block.
func countSummaries(t *testing.T, m *Manager) int {
	t.Helper()
	b, ok := m.GetBlock(BlockRecent, "recent")
	if !ok {
		t.Fatal("recent block missing")
	}
	n := 0
	for _, msg := range b.Messages {
		if isCompactedSummary(msg) {
			n++
		}
	}
	return n
}

// 多轮压缩:摘要必须逐代串联(GEN1 -> GEN2 -> ...),且上下文中恒为一条。
func TestRepeatedFoldsChainSummaries(t *testing.T) {
	gen := 0
	var priors []string
	m := NewManager(TokenBudget{MaxTotal: 2400, TargetTotal: 1000, ReserveOutput: 200},
		WithSummarizer(func(_ context.Context, _ []models.AgentMessage, prior string) (string, error) {
			gen++
			priors = append(priors, prior)
			return fmt.Sprintf("GEN%d", gen), nil
		}),
		WithMinRecent(4),
	)
	m.SetBlock(NewBlock(BlockRecent, "recent", StabilityDynamic, 100, bigRecent(20)...))

	for round := 1; round <= 4; round++ {
		if c, _ := m.MaybeCompact(); !c {
			t.Fatalf("round %d should commit", round)
		}
		if n := countSummaries(t, m); n != 1 {
			t.Fatalf("round %d: expected exactly 1 summary, got %d", round, n)
		}
		b, _ := m.GetBlock(BlockRecent, "recent")
		b.Messages = append(b.Messages, bigRecent(20)...)
	}

	want := []string{"", "GEN1", "GEN2", "GEN3"}
	if len(priors) != len(want) {
		t.Fatalf("expected %d folds, got %d", len(want), len(priors))
	}
	for i, w := range want {
		if priors[i] != w {
			t.Errorf("fold %d: prior = %q, want %q (summary chain broken)", i+1, priors[i], w)
		}
	}
}

// 回归:切点由 token 预算决定,已有摘要可能落在保留尾部而非折叠区间。此时它必须
// 仍被当作 prior 传入并从尾部移除 —— 否则新旧两条摘要同时留在上下文里(旧的还排在
// 新的之后),且本次折叠的 prior 为空,静默丢弃旧摘要记录的一切。
func TestFoldPicksUpSummaryFromKeptTail(t *testing.T) {
	var gotPrior string
	var sawSummaryInSpan bool
	m := NewManager(TokenBudget{MaxTotal: 2400, TargetTotal: 1000, ReserveOutput: 200},
		WithSummarizer(func(_ context.Context, msgs []models.AgentMessage, prior string) (string, error) {
			gotPrior = prior
			for _, msg := range msgs {
				if isCompactedSummary(msg) {
					sawSummaryInSpan = true
				}
			}
			return "NEW", nil
		}),
		WithMinRecent(4),
	)

	// 摘要靠近末尾,按 token 预算从尾部反向切必然保留它。
	msgs := bigRecent(20)
	msgs = append(msgs, models.NewAgentMessage(models.RoleSystem,
		models.TextContent{Text: summaryDisplayPrefix + "OLD SUMMARY"}).
		WithMetadata("compacted", true))
	msgs = append(msgs, bigRecent(4)...)
	m.SetBlock(NewBlock(BlockRecent, "recent", StabilityDynamic, 100, msgs...))

	if c, _ := m.MaybeCompact(); !c {
		t.Fatal("expected compaction to commit")
	}
	if gotPrior != "OLD SUMMARY" {
		t.Errorf("prior = %q, want %q: a summary in the kept tail must still be carried forward", gotPrior, "OLD SUMMARY")
	}
	if sawSummaryInSpan {
		t.Error("the prior summary must not also appear in the transcript span")
	}
	if n := countSummaries(t, m); n != 1 {
		t.Fatalf("expected exactly 1 summary after the fold, got %d", n)
	}
}
