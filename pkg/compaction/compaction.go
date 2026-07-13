package compaction

import (
	"context"
	"fmt"

	"github.com/lcoder/lcoder/pkg/models"
)

// Strategy decides which messages to compact.
type Strategy interface {
	// Compact takes the current messages and returns a new slice where older
	// messages are replaced by a summary system message. It also returns the
	// summary text that should be injected.
	Compact(ctx context.Context, messages []models.AgentMessage, summarize SummarizeFunc) ([]models.AgentMessage, error)
}

// SummarizeFunc generates a summary from a slice of messages.
// In production this calls the LLM engine. The context carries the agent's
// run cancellation so abort/Ctrl+C interrupts in-flight summarization.
type SummarizeFunc func(ctx context.Context, messages []models.AgentMessage) (string, error)

// KeepRecent keeps the last N messages and summarizes the rest.
type KeepRecent struct {
	KeepCount int
}

// NewKeepRecent creates a compaction strategy.
func NewKeepRecent(keep int) *KeepRecent {
	if keep < 1 {
		keep = 1
	}
	return &KeepRecent{KeepCount: keep}
}

// NewKeepLastStrategy creates a compaction strategy that keeps the last N messages.
func NewKeepLastStrategy(keep int) *KeepRecent {
	return NewKeepRecent(keep)
}

// Compact summarizes older messages and appends a system message with the summary.
func (k *KeepRecent) Compact(ctx context.Context, messages []models.AgentMessage, summarize SummarizeFunc) ([]models.AgentMessage, error) {
	if len(messages) <= k.KeepCount {
		return messages, nil
	}

	older := messages[:len(messages)-k.KeepCount]
	recent := messages[len(messages)-k.KeepCount:]

	summaryText, err := summarize(ctx, older)
	if err != nil {
		return nil, fmt.Errorf("summarize: %w", err)
	}

	summary := models.NewAgentMessage(models.RoleSystem, models.TextContent{
		Text: fmt.Sprintf("[Summary of earlier conversation]\n\n%s", summaryText),
	})
	if summary.Metadata == nil {
		summary.Metadata = make(map[string]any)
	}
	summary.Metadata["compacted"] = true

	return append([]models.AgentMessage{summary}, recent...), nil
}

// SimpleSummarize is a placeholder summarizer.
func SimpleSummarize(_ context.Context, messages []models.AgentMessage) (string, error) {
	var texts []string
	for _, m := range messages {
		if m.Role == models.RoleUser || m.Role == models.RoleAssistant {
			texts = append(texts, fmt.Sprintf("%s: %s", m.Role, m.Text()))
		}
	}
	if len(texts) == 0 {
		return "No earlier messages.", nil
	}
	return fmt.Sprintf("Earlier conversation contained %d messages.\n\n%s", len(texts), joinLimited(texts, 2000)), nil
}

func joinLimited(texts []string, maxLen int) string {
	var out string
	for _, t := range texts {
		if len(out)+len(t)+1 > maxLen {
			break
		}
		if out != "" {
			out += "\n"
		}
		out += t
	}
	return out
}
