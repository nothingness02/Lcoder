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

// 扩展 guard policy 参与权限判定:deny 生效,工具不执行。
func TestExtraGuardPolicyDenies(t *testing.T) {
	echo := &echoTool{}
	reg := tools.NewRegistry(t.TempDir())
	reg.Register("echo", echo)

	toolMsg := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Type: "tool_call", ID: "call_1", Name: "echo", Arguments: map[string]any{"command": "x"},
	})
	client := llmtest.Client(llmtest.Turn(llmtest.Done(toolMsg, nil)))

	denyAll := policyFunc(func(permissions.Request) (permissions.Decision, string, bool) {
		return permissions.Deny, "denied by extension", true
	})
	ag := New(Config{
		SystemPrompt:       "x",
		Model:              models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ExtraGuardPolicies: []permissions.Policy{denyAll},
		ShouldStop:         func(context.Context, TurnSummary) (bool, error) { return true, nil },
	}, client, reg, permissions.NewEngine(permissions.DefaultConfig()), events.New())

	if err := ag.Prompt(context.Background(), models.UserMessage("go")); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if echo.gotArgs != nil {
		t.Fatal("denied tool must not execute")
	}
}

type policyFunc func(permissions.Request) (permissions.Decision, string, bool)

func (f policyFunc) Name() string { return "test-extension" }
func (f policyFunc) Decide(r permissions.Request) (permissions.Decision, string, bool) {
	return f(r)
}
