package contextmgr

import (
	"context"
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
		WithSummarizer(func(_ context.Context, msgs []models.AgentMessage) (string, error) {
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
	mgr := NewManager(TokenBudget{MaxTotal: 2400, TargetTotal: 1000, ReserveOutput: 200},
		WithSummarizer(func(_ context.Context, msgs []models.AgentMessage) (string, error) {
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
		WithSummarizer(func(_ context.Context, msgs []models.AgentMessage) (string, error) { return "x", nil }),
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
