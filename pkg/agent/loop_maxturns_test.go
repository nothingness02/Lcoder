package agent

import (
	"context"
	"fmt"
	"testing"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/llm/provider"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/tools"
)

// 超限是硬终局:run 以 max_turns 结束,LastEndReason 可读出,continuation
// hook 不被调用(不过链)。
func TestMaxTurnsPerRunHardStop(t *testing.T) {
	// 每轮都产出一个 tool call,若不限 turn 会无限循环。
	scripted := make([][]provider.Event, 0, 10)
	for i := 0; i < 10; i++ {
		msg := models.NewAgentMessage(models.RoleAssistant,
			models.ToolCallContent{Type: "tool_call", ID: fmt.Sprintf("c%d", i), Name: "echo", Arguments: map[string]any{}})
		scripted = append(scripted, llmtest.Turn(llmtest.Done(msg, nil)))
	}
	client := llmtest.Client(scripted...)

	hookCalled := false
	ag := New(Config{
		SystemPrompt:   "x",
		Model:          models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		MaxTurnsPerRun: 3,
		ShouldContinueAfterStop: func(context.Context, StopContext) (bool, error) {
			hookCalled = true
			return true, nil
		},
	}, client, tools.NewRegistry(t.TempDir()), permissions.NewEngine(permissions.DefaultConfig()), events.New())

	if err := ag.Prompt(context.Background(), models.UserMessage("go")); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if got := ag.LastEndReason(); got != events.EndReasonMaxTurns {
		t.Fatalf("LastEndReason = %q, want %q", got, events.EndReasonMaxTurns)
	}
	if hookCalled {
		t.Fatal("max_turns must not pass the continuation chain")
	}
}
