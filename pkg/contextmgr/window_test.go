package contextmgr

import (
	"context"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

// staticRatioCap drops the lowest-priority static/stable blocks when their total
// exceeds StaticRatio% of the effective input window. The system block, having the
// highest priority, should be the last one removed.
func TestStaticRatioCapsStableBlocks(t *testing.T) {
	mgr := NewManager(TokenBudget{
		MaxTotal:      1000,
		ReserveOutput: 0,
		StaticRatio:   30,
	})

	mgr.SetBlock(NewBlock(BlockSystem, "system", StabilityStatic, 100,
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: strings.Repeat("s", 800)}),
	))
	mgr.SetBlock(NewBlock(BlockProjectDocs, "project_docs", StabilityStable, 80,
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: strings.Repeat("p", 800)}),
	))
	mgr.SetBlock(NewBlock(BlockSkills, "skills", StabilityStable, 90,
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: strings.Repeat("k", 800)}),
	))
	mgr.ReplaceRecent([]models.AgentMessage{models.UserMessage("hi")})

	req, err := mgr.BuildTurnRequest(models.ModelRef{ID: "test"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.SystemPrompt == "" {
		t.Fatal("expected a non-empty system prompt")
	}
	if strings.Contains(req.SystemPrompt, strings.Repeat("p", 800)) {
		t.Fatal("lowest-priority project_docs block should have been dropped")
	}
	if strings.Contains(req.SystemPrompt, strings.Repeat("k", 800)) {
		t.Fatal("skills block should have been dropped after project_docs")
	}
	if !strings.Contains(req.SystemPrompt, strings.Repeat("s", 800)) {
		t.Fatal("system block should be retained")
	}
	if len(req.Messages) == 0 || req.Messages[len(req.Messages)-1].Role != models.RoleUser {
		t.Fatal("recent user message must be preserved")
	}
}

// With StaticRatio disabled, all static/stable blocks fit as before.
func TestStaticRatioDisabledKeepsAllStableBlocks(t *testing.T) {
	mgr := NewManager(TokenBudget{
		MaxTotal:      1000,
		ReserveOutput: 0,
		StaticRatio:   0,
	})

	mgr.SetBlock(NewBlock(BlockSystem, "system", StabilityStatic, 100,
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: strings.Repeat("s", 80)}),
	))
	mgr.SetBlock(NewBlock(BlockProjectDocs, "project_docs", StabilityStable, 80,
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: strings.Repeat("p", 80)}),
	))
	mgr.ReplaceRecent([]models.AgentMessage{models.UserMessage("hi")})

	req, _ := mgr.BuildTurnRequest(models.ModelRef{ID: "test"}, nil)
	if !strings.Contains(req.SystemPrompt, strings.Repeat("p", 80)) {
		t.Fatal("project_docs block should be kept when static_ratio is disabled")
	}
}

// A compacted/truncated tail must never begin with a tool_result whose matching
// tool_use was cut off, since Anthropic rejects an orphan tool_result. The
// window policy strips such leading orphans.
func TestWindowStripsLeadingOrphanToolResult(t *testing.T) {
	orphan := models.NewAgentMessage(models.RoleToolResult,
		models.ToolResultContent{ToolCallID: "x", Content: []models.ContentPart{models.TextContent{Text: "orphan result"}}})

	msgs := []models.AgentMessage{
		orphan,
		orphan,
		models.UserMessage("real user turn"),
		models.AssistantMessage("reply"),
	}
	got := stripLeadingOrphanToolResults(msgs)
	if len(got) != 2 {
		t.Fatalf("expected 2 messages after stripping orphans, got %d", len(got))
	}
	if got[0].Role != models.RoleUser {
		t.Fatalf("expected tail to start with a user message, got %v", got[0].Role)
	}
}

// A paired tool_result (preceded by its assistant tool_use within the tail)
// must be preserved.
func TestWindowKeepsPairedToolResult(t *testing.T) {
	msgs := []models.AgentMessage{
		models.NewAgentMessage(models.RoleAssistant,
			models.ToolCallContent{ID: "x", Name: "read", Arguments: map[string]any{}}),
		models.NewAgentMessage(models.RoleToolResult,
			models.ToolResultContent{ToolCallID: "x", Content: []models.ContentPart{models.TextContent{Text: "ok"}}}),
	}
	got := stripLeadingOrphanToolResults(msgs)
	if len(got) != 2 || got[0].Role != models.RoleAssistant {
		t.Fatalf("paired tool_use/tool_result must be kept intact, got %v", got)
	}
}

// The window policy no longer summarizes; it only truncates to fit the hard
// limit. Even with a summarizer configured on the manager, BuildTurnRequest must
// inject no compacted summary and must keep the request within budget.
func TestWindowTruncatesWithoutSummarizing(t *testing.T) {
	mgr := NewManager(TokenBudget{
		MaxTotal:      2000,
		TargetTotal:   1500,
		ReserveOutput: 500,
	}, WithSummarizer(func(_ context.Context, msgs []models.AgentMessage) (string, error) {
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

func TestDropThresholdTruncatesEarlier(t *testing.T) {
	big := strings.Repeat("word ", 200) // ~200 tokens under DefaultEstimator
	msgs := []models.AgentMessage{}
	for i := 0; i < 10; i++ {
		msgs = append(msgs, models.NewAgentMessage(models.RoleUser, models.TextContent{Text: big}))
	}

	mgrA := NewManager(TokenBudget{MaxTotal: 5000, ReserveOutput: 0})
	mgrA.ReplaceRecent(msgs)
	reqA, _ := mgrA.BuildTurnRequest(models.ModelRef{ID: "test"}, nil)

	mgrB := NewManager(TokenBudget{MaxTotal: 5000, ReserveOutput: 0, DropThreshold: 0.25})
	mgrB.ReplaceRecent(msgs)
	reqB, _ := mgrB.BuildTurnRequest(models.ModelRef{ID: "test"}, nil)

	if len(reqB.Messages) >= len(reqA.Messages) {
		t.Fatalf("drop_threshold=0.25 should truncate more than default, got A=%d B=%d", len(reqA.Messages), len(reqB.Messages))
	}
}
