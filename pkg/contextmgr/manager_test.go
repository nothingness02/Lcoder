package contextmgr

import (
	"context"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func TestManagerBuildTurnRequest(t *testing.T) {
	mgr := NewManager(TokenBudget{
		MaxTotal:      4000,
		TargetTotal:   3000,
		ReserveOutput: 1000,
	}, WithSummarizer(func(_ context.Context, msgs []models.AgentMessage, _ string) (string, error) {
		return "summary", nil
	}))

	mgr.SetBlock(NewBlock(BlockSystem, "system", StabilityStatic, 100,
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: strings.Repeat("a", 400)}),
	))
	mgr.SetBlock(NewBlock(BlockProjectDocs, "docs", StabilityStable, 80,
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: strings.Repeat("b", 400)}),
	))
	mgr.SetBlock(NewBlock(BlockRecent, "recent", StabilityDynamic, 100,
		models.UserMessage("hello"),
		models.AssistantMessage("hi"),
		models.UserMessage("what is the weather"),
	))

	req, err := mgr.BuildTurnRequest(models.ModelRef{Provider: "openai", ID: "gpt-4o"}, nil)
	if err != nil {
		t.Fatalf("build turn request: %v", err)
	}

	if !strings.Contains(req.SystemPrompt, "aaaaaaaa") {
		t.Fatalf("expected system prompt to contain static block")
	}
	if !strings.Contains(req.SystemPrompt, "bbbbbbbb") {
		t.Fatalf("expected system prompt to contain docs block")
	}
	if len(req.Messages) == 0 {
		t.Fatalf("expected messages")
	}
}

func TestStats(t *testing.T) {
	mgr := NewManager(TokenBudget{MaxTotal: 1000})
	mgr.SetBlock(NewBlock(BlockSystem, "system", StabilityStatic, 100,
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: strings.Repeat("x", 400)}),
	))
	mgr.SetBlock(NewBlock(BlockRecent, "recent", StabilityDynamic, 100,
		models.UserMessage("hi"),
	))

	stats := mgr.Stats()
	if stats["total"] == 0 {
		t.Fatal("expected non-zero total tokens")
	}
	if stats["budget_max"] != 1000 {
		t.Fatalf("expected budget_max 1000, got %d", stats["budget_max"])
	}
}

func TestSetThinkingRoundTrip(t *testing.T) {
	mgr := NewManager(TokenBudget{MaxTotal: 1000}, WithThinking("low"))
	if got := mgr.Thinking(); got != "low" {
		t.Fatalf("initial thinking = %q, want low", got)
	}
	mgr.SetThinking("high")
	if got := mgr.Thinking(); got != "high" {
		t.Fatalf("after SetThinking = %q, want high", got)
	}
	// The new value flows onto the next turn request.
	mgr.SetBlock(NewBlock(BlockRecent, "recent", StabilityDynamic, 100, models.UserMessage("hi")))
	req, err := mgr.BuildTurnRequest(models.ModelRef{Provider: "openai", ID: "gpt-4o"}, nil)
	if err != nil {
		t.Fatalf("build turn request: %v", err)
	}
	if req.Thinking != "high" {
		t.Fatalf("turn request thinking = %q, want high", req.Thinking)
	}
}
