package agent

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/observability"
	"github.com/lcoder/lcoder/pkg/permissions"
)

// Tool-call argument deltas must be marked IsToolCall so UI consumers can
// keep the raw JSON out of the visible transcript.
func TestToolCallDeltaMarkedNotText(t *testing.T) {
	toolMsg := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Type: "tool_call", ID: "call_1", Name: "ls", Arguments: map[string]any{},
	})
	client := llmtest.Client(llmtest.Turn(
		llmtest.Start(),
		llmtest.ToolCall(0, `{"path":"pkg/extension/loader.go"}`),
		llmtest.Done(toolMsg, nil),
	))

	bus := events.New()
	var updates []events.MessageUpdateEvent
	bus.Subscribe(func(ctx context.Context, ev events.Event) error {
		if u, ok := ev.(events.MessageUpdateEvent); ok {
			updates = append(updates, u)
		}
		return nil
	})

	obs := observability.NewCollector(observability.NewMemoryExporter())
	ag := NewWithObservability(Config{
		SystemPrompt:      "You are helpful.",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionParallel,
		ShouldStop: func(ctx context.Context, turn TurnSummary) (bool, error) {
			return true, nil
		},
	}, client, testRegistry(t.TempDir()), permissions.NewEngine(permissions.DefaultConfig()), bus, obs)

	if err := ag.Prompt(context.Background(), models.UserMessage("read it")); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	if len(updates) == 0 {
		t.Fatal("expected tool-call delta events")
	}
	for _, u := range updates {
		if !u.IsToolCall {
			t.Fatalf("tool-call delta not marked IsToolCall: %+v", u)
		}
	}
}
