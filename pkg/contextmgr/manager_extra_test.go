package contextmgr

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func TestManagerAllMessages(t *testing.T) {
	m := NewManager(TokenBudget{MaxTotal: 1000})
	m.SetSystemPrompt("you are an agent")
	m.AppendRecent(models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hello"}))
	m.AppendRecent(models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "hi"}))

	msgs := m.AllMessages()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != models.RoleUser {
		t.Fatalf("expected user first, got %s", msgs[0].Role)
	}
}

func TestManagerSetMessages(t *testing.T) {
	m := NewManager(TokenBudget{MaxTotal: 1000})
	m.SetMessages([]models.AgentMessage{
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: "sys"}),
		models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "u"}),
		models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "a"}),
	})

	if m.SystemPrompt() != "sys" {
		t.Fatalf("unexpected system prompt: %s", m.SystemPrompt())
	}
	if len(m.AllMessages()) != 2 {
		t.Fatalf("expected 2 non-system messages, got %d", len(m.AllMessages()))
	}
}

func TestManagerClone(t *testing.T) {
	m := NewManager(TokenBudget{MaxTotal: 1000})
	m.AppendRecent(models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hello"}))

	other := m.Clone()
	other.AppendRecent(models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "world"}))

	if len(m.AllMessages()) != 1 {
		t.Fatalf("original should have 1 message, got %d", len(m.AllMessages()))
	}
	if len(other.AllMessages()) != 2 {
		t.Fatalf("clone should have 2 messages, got %d", len(other.AllMessages()))
	}
}

// Clone is only reached from Agent.WithMode, so any configured field it fails to
// carry over is silently reset by a mode switch: caching would revert to the
// default policy, thinking would change how the request is generated, and the
// compaction budgets would jump back to their defaults mid-session.
func TestCloneCarriesConfiguration(t *testing.T) {
	m := NewManager(TokenBudget{MaxTotal: 9999, TargetTotal: 9000, ReserveOutput: 200},
		WithCacheHintPolicy(CachePolicyNone),
		WithMinRecent(7),
		WithKeepRecentTokens(1234),
		WithThinking("high"),
		WithSummarizer(func(_ context.Context, _ []models.AgentMessage, _ string) (string, error) {
			return "s", nil
		}),
	)
	m.AddEphemeralReminder("PENDING")

	c := m.Clone()

	if c.cachePolicy != CachePolicyNone {
		t.Errorf("cachePolicy = %q, want %q", c.cachePolicy, CachePolicyNone)
	}
	if c.keepRecent != 7 {
		t.Errorf("keepRecent = %d, want 7", c.keepRecent)
	}
	if c.keepRecentTokens != 1234 {
		t.Errorf("keepRecentTokens = %d, want 1234", c.keepRecentTokens)
	}
	if c.thinking != "high" {
		t.Errorf("thinking = %q, want %q", c.thinking, "high")
	}
	if got := c.EphemeralReminders(); len(got) != 1 || got[0] != "PENDING" {
		t.Errorf("ephemeral reminders = %v, want [PENDING]", got)
	}

	// The clone must be independent: mutating it cannot affect the source.
	c.AddEphemeralReminder("CLONE ONLY")
	if len(m.EphemeralReminders()) != 1 {
		t.Error("clone shares the reminder slice with its source")
	}
}
