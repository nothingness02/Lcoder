package contextmgr

import (
	"context"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func TestManagerSnapshotRestore(t *testing.T) {
	original := NewManager(TokenBudget{
		MaxTotal:    4000,
		TargetTotal: 3000,
	}, WithEstimator(DefaultEstimator), WithSummarizer(func(_ context.Context, msgs []models.AgentMessage, _ string) (string, error) {
		return "summary", nil
	}), WithWindowPolicy(&KeepRecentInBudget{}))

	original.SetSystemPrompt("you are a helpful agent")
	original.AppendRecent(models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hello"}))

	state, err := original.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	fresh := NewManager(TokenBudget{}, WithEstimator(DefaultEstimator), WithSummarizer(func(_ context.Context, msgs []models.AgentMessage, _ string) (string, error) {
		return "summary", nil
	}), WithWindowPolicy(&KeepRecentInBudget{}))

	if err := fresh.Restore(state); err != nil {
		t.Fatalf("restore: %v", err)
	}

	if fresh.Budget().MaxTotal != original.Budget().MaxTotal {
		t.Fatalf("budget max total mismatch: got %d, want %d", fresh.Budget().MaxTotal, original.Budget().MaxTotal)
	}
	if fresh.Budget().TargetTotal != original.Budget().TargetTotal {
		t.Fatalf("budget target total mismatch: got %d, want %d", fresh.Budget().TargetTotal, original.Budget().TargetTotal)
	}

	if !strings.Contains(fresh.SystemPrompt(), "helpful agent") {
		t.Fatalf("expected system prompt to be preserved, got %q", fresh.SystemPrompt())
	}

	if len(fresh.AllMessages()) != 1 {
		t.Fatalf("expected 1 recent message after restore, got %d", len(fresh.AllMessages()))
	}

	// Mutating the original should not affect the restored copy.
	original.AppendRecent(models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "hi"}))
	if len(fresh.AllMessages()) != 1 {
		t.Fatalf("restore should be independent of original")
	}
}

func TestManagerRestoreRejectsMissingServices(t *testing.T) {
	mgr := NewManager(TokenBudget{})

	state := &ManagerState{
		Budget: TokenBudget{MaxTotal: 1000},
		Blocks: []BlockState{
			{
				Kind:     BlockSystem,
				Name:     "system",
				Priority: 100,
				Stability: StabilityStatic,
				Messages: []models.AgentMessage{
					models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: "sys"}),
				},
			},
		},
	}

	if err := mgr.Restore(state); err == nil {
		t.Fatalf("expected restore to reject missing services")
	}
}
