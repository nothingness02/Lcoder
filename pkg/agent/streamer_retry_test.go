package agent

import (
	"context"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/observability"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/tools"
)

func TestStreamTurnRetryPreContentError(t *testing.T) {
	okMsg := models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "recovered"})
	client, adapter := llmtest.NewScript(
		llmtest.Turn(llmtest.ErrorEvent("rate_limit", "slow down")),
		llmtest.Turn(llmtest.Start(), llmtest.Text("recovered"), llmtest.Done(okMsg, nil)),
	)
	ag := NewWithObservability(Config{
		SystemPrompt: "You are helpful.",
		Model:        models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
	}, client, tools.NewRegistry(t.TempDir()), permissions.NewEngine(permissions.DefaultConfig()), events.New(),
		observability.NewCollector(observability.NewMemoryExporter()))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Prompt 本身不返回流错误(loop 发 ErrorEvent 后结束 run),
	// 重试行为只能通过 adapter 调用次数观察。
	_ = ag.Prompt(ctx, models.UserMessage("hi"))
	if adapter.CallCount() != 2 {
		t.Fatalf("pre-content rate_limit should be retried, want 2 turn attempts, got %d", adapter.CallCount())
	}
}

func TestStreamNoRetryAfterContentStarted(t *testing.T) {
	client, adapter := llmtest.NewScript(
		llmtest.Turn(llmtest.Start(), llmtest.Text("partial"), llmtest.ErrorEvent("rate_limit", "slow down")),
	)
	ag := NewWithObservability(Config{
		SystemPrompt: "You are helpful.",
		Model:        models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
	}, client, tools.NewRegistry(t.TempDir()), permissions.NewEngine(permissions.DefaultConfig()), events.New(),
		observability.NewCollector(observability.NewMemoryExporter()))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = ag.Prompt(ctx, models.UserMessage("hi"))
	if adapter.CallCount() != 1 {
		t.Fatalf("mid-stream error must not retry, got %d calls", adapter.CallCount())
	}
}
