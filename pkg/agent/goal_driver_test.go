package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/llm/provider"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/tools"
)

// 模型在第二个 run 里 update_goal complete → driver 退出,状态 complete,
// 且第二个 run 的输入是 continuation prompt。
func TestGoalDriverRunsUntilComplete(t *testing.T) {
	natural := models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "did some work"})
	complete := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Type: "tool_call", ID: "c1", Name: "update_goal",
		Arguments: map[string]any{"status": "complete"},
	})
	finale := models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "all done"})
	client := llmtest.Client(
		llmtest.Turn(llmtest.Done(natural, nil)),
		llmtest.Turn(llmtest.Done(complete, nil)),
		llmtest.Turn(llmtest.Done(finale, nil)),
	)

	ag := New(Config{
		SystemPrompt: "x",
		Model:        models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
	}, client, testRegistry("."), permissions.NewEngine(permissions.DefaultConfig()), events.New())

	d := NewGoalDriver(ag)
	if err := d.Run(context.Background(), "fix the test", 0, 0); err != nil {
		t.Fatalf("driver: %v", err)
	}
	if g := ag.Goal(); g == nil || g.Status != GoalComplete {
		t.Fatalf("Status = %v, want complete", g)
	}
	// 第二个 run 的用户消息必须是 continuation prompt。
	var sawContinuation bool
	for _, m := range ag.AllMessages() {
		if m.Role == models.RoleUser && strings.Contains(m.Text(), GoalContinuationPromptText) {
			sawContinuation = true
		}
	}
	if !sawContinuation {
		t.Fatal("continuation prompt was never injected")
	}
}

// turn 预算耗尽 → driver 标记 blocked 并退出。
func TestGoalDriverStopsOnBudget(t *testing.T) {
	natural := models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "working"})
	scripted := make([][]provider.Event, 0, 10)
	for i := 0; i < 10; i++ {
		scripted = append(scripted, llmtest.Turn(llmtest.Done(natural, nil)))
	}
	client := llmtest.Client(scripted...)

	ag := New(Config{
		SystemPrompt: "x",
		Model:        models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
	}, client, tools.NewRegistry(t.TempDir()), permissions.NewEngine(permissions.DefaultConfig()), events.New())

	d := NewGoalDriver(ag)
	if err := d.Run(context.Background(), "endless task", 2, 0); err != nil {
		t.Fatalf("driver: %v", err)
	}
	g := ag.Goal()
	if g == nil || g.Status != GoalBlocked {
		t.Fatalf("Status = %v, want blocked", g)
	}
	if g.TurnsUsed > 2 {
		t.Fatalf("TurnsUsed = %d, budget 2 must cap the runs", g.TurnsUsed)
	}
}

// 用户中断 → goal paused,driver 退出。
func TestGoalDriverPauseOnAbort(t *testing.T) {
	natural := models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "working"})
	scripted := make([][]provider.Event, 0, 10)
	for i := 0; i < 10; i++ {
		scripted = append(scripted, llmtest.Turn(llmtest.Done(natural, nil)))
	}
	client := llmtest.Client(scripted...)

	ag := New(Config{
		SystemPrompt: "x",
		Model:        models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
	}, client, tools.NewRegistry(t.TempDir()), permissions.NewEngine(permissions.DefaultConfig()), events.New())

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即中断
	d := NewGoalDriver(ag)
	_ = d.Run(ctx, "task", 0, 0)
	if g := ag.Goal(); g == nil || g.Status != GoalPaused {
		t.Fatalf("Status = %v, want paused", g)
	}
}
