package agent

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
)

// schemaToolMsg builds an assistant message that calls "edit" with the given
// arguments. "edit" requires both "path" and "edits".
func schemaToolMsg(args map[string]any) models.AgentMessage {
	return models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Type: "tool_call", ID: "call_1", Name: "edit", Arguments: args,
	})
}

func TestExecuteToolCall_InvalidArgs_NoToolEvents(t *testing.T) {
	// Assistant calls edit with a missing required field ("edits").
	toolMsg := schemaToolMsg(map[string]any{"path": "main.go"})
	client := llmtest.Client(llmtest.Turn(llmtest.Done(toolMsg, nil)))

	bus := events.New()
	var sawStart, sawEnd bool
	bus.Subscribe(func(_ context.Context, ev events.Event) error {
		switch ev.EventType() {
		case events.ToolExecutionStart:
			sawStart = true
		case events.ToolExecutionEnd:
			sawEnd = true
		}
		return nil
	})

	ag := New(Config{
		SystemPrompt:      "x",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionParallel,
		ShouldStop: func(context.Context, TurnSummary) (bool, error) {
			return true, nil
		},
	}, client, testRegistry("."), permissions.NewEngine(permissions.DefaultConfig()), bus)

	if err := ag.Prompt(context.Background(), models.UserMessage("go")); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	if sawStart || sawEnd {
		t.Fatalf("validation failure must not emit tool events (start=%v end=%v)", sawStart, sawEnd)
	}

	// The failed call must surface as an error tool_result in context so the LLM
	// can correct on the next turn.
	msgs := ag.AllMessages()
	var foundErr bool
	for _, m := range msgs {
		if m.Role != models.RoleToolResult {
			continue
		}
		if rc, ok := m.Content[0].(models.ToolResultContent); ok && rc.IsError {
			foundErr = true
		}
	}
	if !foundErr {
		t.Fatal("expected an error tool_result fed back for the invalid call")
	}
}

func TestExecuteToolCall_ValidArgs_EmitsEvents(t *testing.T) {
	toolMsg := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Type: "tool_call", ID: "call_1", Name: "ls", Arguments: map[string]any{},
	})
	client := llmtest.Client(llmtest.Turn(llmtest.Done(toolMsg, nil)))

	bus := events.New()
	var sawStart, sawEnd bool
	bus.Subscribe(func(_ context.Context, ev events.Event) error {
		switch ev.EventType() {
		case events.ToolExecutionStart:
			sawStart = true
		case events.ToolExecutionEnd:
			sawEnd = true
		}
		return nil
	})

	ag := New(Config{
		SystemPrompt:      "x",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionParallel,
		ShouldStop: func(context.Context, TurnSummary) (bool, error) {
			return true, nil
		},
	}, client, testRegistry("."), permissions.NewEngine(permissions.DefaultConfig()), bus)

	if err := ag.Prompt(context.Background(), models.UserMessage("go")); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	if !sawStart || !sawEnd {
		t.Fatalf("valid call must emit tool events (start=%v end=%v)", sawStart, sawEnd)
	}
}
