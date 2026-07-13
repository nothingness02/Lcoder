package contextmgr

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

// pressureManager builds a manager whose recent block is far over budget.
func pressureManager(withSummarizer bool) *Manager {
	opts := []Option{WithMinRecent(2)}
	if withSummarizer {
		opts = append(opts, WithSummarizer(stubSummarizer))
	}
	m := NewManager(TokenBudget{MaxTotal: 2000, TargetTotal: 100, ReserveOutput: 0}, opts...)
	var recent []models.AgentMessage
	for i := 0; i < 20; i++ {
		recent = append(recent, models.UserMessage(strings.Repeat("u", 200)))
		recent = append(recent, models.AssistantMessage(strings.Repeat("a", 200)))
	}
	m.SetBlock(NewBlock(BlockRecent, "recent", StabilityDynamic, 100, recent...))
	return m
}

// PendingCompaction 与 MaybeCompactLeveled 的守卫一致:只有在压缩真正会
// 执行时才返回非 None 级别,供调用方提前发"压缩开始"信号。
func TestPendingCompaction(t *testing.T) {
	if got := pressureManager(true).PendingCompaction(); got == CompactionNone {
		t.Fatal("expected non-None level under pressure")
	}

	// 无 summarizer:压缩不会执行 → None。
	if got := pressureManager(false).PendingCompaction(); got != CompactionNone {
		t.Fatalf("no summarizer must yield None, got %v", got)
	}

	// 消息太少:< minLeveledMessages → None。
	small := NewManager(TokenBudget{MaxTotal: 2000, TargetTotal: 100, ReserveOutput: 0},
		WithSummarizer(stubSummarizer), WithMinRecent(2))
	small.SetBlock(NewBlock(BlockRecent, "recent", StabilityDynamic, 100,
		models.UserMessage(strings.Repeat("u", 800))))
	if got := small.PendingCompaction(); got != CompactionNone {
		t.Fatalf("below minLeveledMessages must yield None, got %v", got)
	}

	// 无压力 → None。
	healthy := NewManager(TokenBudget{MaxTotal: 100000, TargetTotal: 90000, ReserveOutput: 1000},
		WithSummarizer(stubSummarizer))
	var msgs []models.AgentMessage
	for i := 0; i < 6; i++ {
		msgs = append(msgs, models.UserMessage("hi"), models.AssistantMessage("ok"))
	}
	healthy.SetBlock(NewBlock(BlockRecent, "recent", StabilityDynamic, 100, msgs...))
	if got := healthy.PendingCompaction(); got != CompactionNone {
		t.Fatalf("no pressure must yield None, got %v", got)
	}
}
