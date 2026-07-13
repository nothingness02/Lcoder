package compaction

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func TestKeepRecentNoCompact(t *testing.T) {
	strategy := NewKeepRecent(3)
	messages := []models.AgentMessage{
		models.UserMessage("a"),
		models.UserMessage("b"),
	}
	result, err := strategy.Compact(context.Background(), messages, SimpleSummarize)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
}

func TestKeepRecentCompact(t *testing.T) {
	strategy := NewKeepRecent(1)
	messages := []models.AgentMessage{
		models.UserMessage("a"),
		models.UserMessage("b"),
		models.UserMessage("c"),
	}
	result, err := strategy.Compact(context.Background(), messages, SimpleSummarize)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	if result[0].Role != models.RoleSystem {
		t.Fatalf("expected summary system message, got %s", result[0].Role)
	}
	if result[1].Text() != "c" {
		t.Fatalf("expected recent message c, got %s", result[1].Text())
	}
}
