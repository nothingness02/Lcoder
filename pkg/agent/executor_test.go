package agent

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/tools"
)

// echoTool records the args it was executed with so tests can observe what the
// executor actually dispatched.
type echoTool struct {
	gotArgs map[string]any
}

func (e *echoTool) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name:        "echo",
		Description: "Echoes its arguments.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"command": map[string]any{"type": "string"}},
		},
	}
}

func (e *echoTool) Execute(_ context.Context, _ string, args map[string]any) (models.ToolExecutionResult, error) {
	e.gotArgs = args
	return models.NewToolExecutionResultText("ok"), nil
}

func TestExecutorBeforeHookModifiedArgs(t *testing.T) {
	echo := &echoTool{}
	registry := tools.NewRegistry(t.TempDir())
	registry.Register("echo", echo)

	toolMsg := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Type: "tool_call", ID: "call_1", Name: "echo", Arguments: map[string]any{"command": "original"},
	})
	client := llmtest.Client(llmtest.Turn(llmtest.Done(toolMsg, nil)))

	ag := New(Config{
		SystemPrompt:      "x",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionParallel,
		BeforeToolCall: func(_ context.Context, _ ToolCallInfo) (*BeforeToolCallResult, error) {
			return &BeforeToolCallResult{ModifiedArgs: map[string]any{"command": "rewritten"}}, nil
		},
		ShouldStop: func(context.Context, TurnSummary) (bool, error) {
			return true, nil
		},
	}, client, registry, permissions.NewEngine(permissions.DefaultConfig()), events.New())

	if err := ag.Prompt(context.Background(), models.UserMessage("go")); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	if echo.gotArgs["command"] != "rewritten" {
		t.Fatalf("args = %v, hook modification not applied", echo.gotArgs)
	}
}
