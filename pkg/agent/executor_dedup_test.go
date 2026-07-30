package agent

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
)

func TestExecute_DedupReadSameTurn(t *testing.T) {
	toolMsg := models.NewAgentMessage(models.RoleAssistant,
		models.ToolCallContent{Type: "tool_call", ID: "call_1", Name: "ls", Arguments: map[string]any{}},
		models.ToolCallContent{Type: "tool_call", ID: "call_2", Name: "ls", Arguments: map[string]any{}},
	)
	client := llmtest.Client(llmtest.Turn(llmtest.Done(toolMsg, nil)))

	bus := events.New()
	var endCount int
	bus.Subscribe(func(_ context.Context, ev events.Event) error {
		if ev.EventType() == events.ToolExecutionEnd {
			endCount++
		}
		return nil
	})

	ag := New(Config{
		SystemPrompt:      "x",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ShouldStop: func(context.Context, TurnSummary) (bool, error) {
			return true, nil
		},
	}, client, testRegistry(t.TempDir()), permissions.NewEngine(permissions.DefaultConfig()), bus)

	if err := ag.Prompt(context.Background(), models.UserMessage("go")); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	if endCount != 1 {
		t.Fatalf("expected 1 tool execution for dedup, got %d", endCount)
	}

	msgs := ag.AllMessages()
	var dedupFound bool
	for _, m := range msgs {
		if m.Role != models.RoleToolResult {
			continue
		}
		tr, ok := m.Content[0].(models.ToolResultContent)
		if !ok {
			continue
		}
		if tr.ToolCallID == "call_2" && tr.Details != nil && tr.Details["deduplicated"] == true {
			dedupFound = true
		}
	}
	if !dedupFound {
		t.Fatal("expected second read result to be marked deduplicated")
	}
}
