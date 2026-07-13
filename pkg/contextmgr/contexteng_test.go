package contextmgr

import (
	"context"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

// stubSummarizer returns a fixed short summary, deterministically.
func stubSummarizer(_ context.Context, _ []models.AgentMessage) (string, error) {
	return "summary of older messages", nil
}

// convoMsgs builds n alternating user/assistant messages each ~200 chars
// (≈50 tokens under DefaultEstimator) so token totals are easy to reason about.
func convoMsgs(n int) []models.AgentMessage {
	msgs := make([]models.AgentMessage, 0, n)
	for i := 0; i < n; i++ {
		role := models.RoleUser
		if i%2 == 1 {
			role = models.RoleAssistant
		}
		msgs = append(msgs, models.NewAgentMessage(role, models.TextContent{
			Text: strings.Repeat("x", 200),
		}))
	}
	return msgs
}

func TestPressureLevel_Thresholds(t *testing.T) {
	b := TokenBudget{MaxTotal: 1000, ReserveOutput: 0} // EffectiveInput = 1000
	cases := []struct {
		total int
		want  CompactionLevel
	}{
		{850, CompactionNone},
		{900, CompactionProactive},
		{940, CompactionProactive},
		{950, CompactionPreflight},
		{990, CompactionPreflight},
		{1000, CompactionReactive},
		{1200, CompactionReactive},
	}
	for _, c := range cases {
		if got := b.PressureLevel(c.total); got != c.want {
			t.Errorf("PressureLevel(%d) = %v, want %v", c.total, got, c.want)
		}
	}
}

func TestKeepTokensForLevel_ScalesWithPressure(t *testing.T) {
	// Large window so the 30% cap does not bind; budget derives from base.
	m := NewManager(TokenBudget{MaxTotal: 1_000_000, ReserveOutput: 0}, WithKeepRecentTokens(1000))
	if got := m.keepTokensForLevel(CompactionProactive); got != 1000 {
		t.Errorf("proactive keep tokens = %d, want 1000", got)
	}
	if got := m.keepTokensForLevel(CompactionPreflight); got != 500 {
		t.Errorf("preflight keep tokens = %d, want 500", got)
	}
	if got := m.keepTokensForLevel(CompactionReactive); got != 256 {
		t.Errorf("reactive keep tokens = %d, want 256 (1000/5=200 floored to 256)", got)
	}
}

func TestMaybeCompactLeveled_FoldsUnderPressure(t *testing.T) {
	// EffectiveInput = 400. 20 msgs ≈ 1000 tokens → reactive.
	budget := TokenBudget{MaxTotal: 400, ReserveOutput: 0}
	m := NewManager(budget, WithSummarizer(stubSummarizer), WithMinRecent(8))
	m.SetSystemPrompt("sys")
	m.ReplaceRecent(convoMsgs(20))

	level, res, err := m.MaybeCompactLeveled(context.Background())
	if err != nil {
		t.Fatalf("MaybeCompactLeveled: %v", err)
	}
	if level != CompactionReactive {
		t.Fatalf("expected reactive level, got %v", level)
	}
	if !res.Committed {
		t.Fatalf("expected a committed fold under reactive pressure")
	}
	recent, _ := m.GetBlock(BlockRecent, "recent")
	// The fold always preserves the last user message and stays within the
	// reactive token budget, so the result is a drastic shrink from 20 messages
	// down to a summary plus a small tail — at most half the original count.
	if len(recent.Messages) > 10 {
		t.Fatalf("expected reactive fold to shrink 20 messages to at most half, got %d", len(recent.Messages))
	}
	if v, ok := recent.Messages[0].Metadata["compacted"].(bool); !ok || !v {
		t.Fatalf("expected folded summary at head with compacted=true")
	}
	// The retained tail must still end at the last original message.
	if recent.Messages[len(recent.Messages)-1].Text() != strings.Repeat("x", 200) {
		t.Fatalf("expected the fold to retain the final original message")
	}
}

func TestMaybeCompactLeveled_ShortSessionAndNoPressure(t *testing.T) {
	// Short session: fewer than minLeveledMessages → never compacts.
	m := NewManager(TokenBudget{MaxTotal: 10, ReserveOutput: 0}, WithSummarizer(stubSummarizer))
	m.ReplaceRecent(convoMsgs(3))
	if level, res, _ := m.MaybeCompactLeveled(context.Background()); res.Committed || level != CompactionNone {
		t.Fatalf("short session must not compact, got level=%v committed=%v", level, res.Committed)
	}

	// No pressure: huge budget → none.
	big := NewManager(TokenBudget{MaxTotal: 1_000_000, ReserveOutput: 0}, WithSummarizer(stubSummarizer))
	big.ReplaceRecent(convoMsgs(20))
	if level, res, _ := big.MaybeCompactLeveled(context.Background()); res.Committed || level != CompactionNone {
		t.Fatalf("no pressure must not compact, got level=%v committed=%v", level, res.Committed)
	}
}

func TestEphemeralReminder_InjectedThenGone(t *testing.T) {
	m := NewManager(TokenBudget{MaxTotal: 10000, ReserveOutput: 1000})
	m.SetSystemPrompt("you are a test agent")
	m.ReplaceRecent([]models.AgentMessage{
		models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "do the thing"}),
	})
	m.SetEphemeralReminders([]string{"You have 1 unfinished todo. Keep going."})

	req, err := m.BuildTurnRequest(models.ModelRef{ID: "test"}, nil)
	if err != nil {
		t.Fatalf("BuildTurnRequest: %v", err)
	}
	// The reminder rides as the trailing message, wrapped in <system-reminder>.
	last := req.Messages[len(req.Messages)-1]
	if !IsEphemeral(last) {
		t.Fatalf("expected the trailing message to be ephemeral")
	}
	if !strings.Contains(last.Text(), "<system-reminder>") || !strings.Contains(last.Text(), "unfinished todo") {
		t.Fatalf("expected a <system-reminder> envelope, got %q", last.Text())
	}
	// Ephemerality: it must NOT be persisted in any block.
	for _, msg := range m.AllMessages() {
		if IsEphemeral(msg) {
			t.Fatalf("ephemeral reminder leaked into persisted history")
		}
	}
	// The cache breakpoint must anchor the real user turn, never the ephemeral
	// trailing message (its index would be len-1).
	for _, bp := range req.CacheBreakpoints {
		if bp == len(req.Messages)-1 {
			t.Fatalf("cache breakpoint must not anchor the ephemeral message")
		}
	}

	// Next turn after clearing: the reminder is gone.
	m.ClearEphemeralReminders()
	req2, _ := m.BuildTurnRequest(models.ModelRef{ID: "test"}, nil)
	for _, msg := range req2.Messages {
		if IsEphemeral(msg) {
			t.Fatalf("ephemeral reminder survived into the next turn")
		}
	}
}

func TestRecordRealUsage_DrivesAccounting(t *testing.T) {
	m := NewManager(TokenBudget{MaxTotal: 1000, ReserveOutput: 0}, WithSummarizer(stubSummarizer))
	m.SetSystemPrompt("sys")
	m.ReplaceRecent(convoMsgs(4)) // small heuristic estimate

	if _, ok := m.RealPromptTokens(); ok {
		t.Fatalf("expected no real usage before any turn")
	}
	// Before usage, accounting falls back to the heuristic estimate.
	if m.currentTotalTokens() != m.totalTokens() {
		t.Fatalf("expected estimate fallback before usage")
	}

	m.RecordRealUsage(models.LLMUsage{
		PromptTokens:     600, // fresh input
		CacheReadTokens:  300, // cache_read
		CacheWriteTokens: 50,  // cache_creation
	})
	got, ok := m.RealPromptTokens()
	if !ok || got != 950 {
		t.Fatalf("RealPromptTokens = (%d,%v), want (950,true)", got, ok)
	}
	if m.currentTotalTokens() != 950 {
		t.Fatalf("currentTotalTokens should prefer real usage, got %d", m.currentTotalTokens())
	}
	// Real usage (950/1000 = 95%) now drives the compaction pressure level.
	stats := m.Stats()
	if stats["real_prompt_total"] != 950 || stats["real_input"] != 600 ||
		stats["real_cache_read"] != 300 || stats["real_cache_creation"] != 50 {
		t.Fatalf("Stats missing real-token accounting: %+v", stats)
	}
	if stats["compaction_level"] != int(CompactionPreflight) {
		t.Fatalf("expected preflight level from real tokens, got %d", stats["compaction_level"])
	}
}
