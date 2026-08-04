package agent

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
)

func TestGoalOverBudget(t *testing.T) {
	g := &GoalState{Status: GoalActive, TurnBudget: 2, TokenBudget: 100}
	if g.OverBudget() {
		t.Fatal("fresh goal must not be over budget")
	}
	g.TurnsUsed = 2
	if !g.OverBudget() {
		t.Fatal("turn budget reached")
	}
	g.TurnsUsed = 0
	g.RecordUsage(models.LLMUsage{CompletionTokens: 60})
	g.RecordUsage(models.LLMUsage{CompletionTokens: 41})
	if !g.OverBudget() {
		t.Fatal("token budget reached at 101/100")
	}
	if g.TokensUsed != 101 {
		t.Fatalf("TokensUsed = %d, want 101", g.TokensUsed)
	}
}

// run 循环在 turn 边界为 active goal 记账 output token。
func TestGoalAccountingAtTurnBoundary(t *testing.T) {
	msg := models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "done"})
	client := llmtest.Client(llmtest.Turn(llmtest.Done(msg, &models.LLMUsage{CompletionTokens: 42})))

	ag := New(Config{
		SystemPrompt: "x",
		Model:        models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
	}, client, testRegistry("."), permissions.NewEngine(permissions.DefaultConfig()), events.New())
	ag.StartGoal("test goal", 0, 0)

	if err := ag.Prompt(context.Background(), models.UserMessage("go")); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if g := ag.Goal(); g == nil || g.TokensUsed != 42 {
		t.Fatalf("TokensUsed = %v, want 42", g)
	}
}

// update_goal 的状态迁移由 executor 应用:模型自标 complete。
func TestUpdateGoalAppliedByExecutor(t *testing.T) {
	call := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Type: "tool_call", ID: "c1", Name: "update_goal",
		Arguments: map[string]any{"status": "complete"},
	})
	done := models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "finished"})
	client := llmtest.Client(
		llmtest.Turn(llmtest.Done(call, nil)),
		llmtest.Turn(llmtest.Done(done, nil)),
	)

	ag := New(Config{
		SystemPrompt: "x",
		Model:        models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
	}, client, testRegistry("."), permissions.NewEngine(permissions.DefaultConfig()), events.New())
	ag.StartGoal("test goal", 0, 0)

	if err := ag.Prompt(context.Background(), models.UserMessage("go")); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if g := ag.Goal(); g == nil || g.Status != GoalComplete {
		t.Fatalf("Status = %v, want complete", g)
	}
}

// goal active 且超预算时,内置 veto 掐停 continuation(且只停不续)。
func TestGoalBudgetVetoStopsContinuation(t *testing.T) {
	natural := models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "working"})
	client := llmtest.Client(
		llmtest.Turn(llmtest.Done(natural, &models.LLMUsage{CompletionTokens: 150})),
	)

	continued := false
	ag := New(Config{
		SystemPrompt: "x",
		Model:        models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ContinuationDeciders: []ContinuationDecider{
			func(context.Context, StopContext) (bool, error) {
				continued = true
				return true, nil // 软续跑必须被 veto 压住
			},
		},
	}, client, testRegistry("."), permissions.NewEngine(permissions.DefaultConfig()), events.New())
	ag.StartGoal("test goal", 0, 100) // tokenBudget=100,一轮记账 150 → 超

	if err := ag.Prompt(context.Background(), models.UserMessage("go")); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if continued {
		t.Fatal("soft continuation must be vetoed by the over-budget goal")
	}
}

// 无变化的 goal 操作(complete 上 Pause、空 mutate、complete 上 Resume)不得发射
// 内容不变的 GoalUpdatedEvent;CancelGoal 的 nil 快照则必须每次都发射。
func TestGoalNoOpMutationsDoNotEmit(t *testing.T) {
	client := llmtest.Client(llmtest.Turn(llmtest.Done(models.AssistantMessage("ok"), nil)))
	ag := New(Config{
		SystemPrompt: "x",
		Model:        models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
	}, client, testRegistry("."), permissions.NewEngine(permissions.DefaultConfig()), events.New())

	var emitted []events.GoalUpdatedEvent
	ag.Subscribe(func(_ context.Context, ev events.Event) error {
		if g, ok := ev.(events.GoalUpdatedEvent); ok {
			emitted = append(emitted, g)
		}
		return nil
	})

	ag.StartGoal("obj", 0, 0)                                       // +1 (active)
	if _, err := ag.goals.applyUpdate("complete", ""); err != nil { // +1 (complete)
		t.Fatalf("applyUpdate: %v", err)
	}
	ag.PauseGoal("nope")                   // complete goal: no-op, no event
	ag.ResumeGoal()                        // complete goal: no-op, no event
	ag.BlockGoal("nope")                   // complete goal: no-op, no event
	ag.goals.mutate(func(g *GoalState) {}) // zero change: no event
	if len(emitted) != 2 {
		t.Fatalf("no-op mutations emitted events: %d events: %+v", len(emitted), emitted)
	}

	ag.CancelGoal() // +1 (cleared)
	ag.CancelGoal() // +1: nil snapshot must always fire, even when already cleared
	if len(emitted) != 4 {
		t.Fatalf("CancelGoal must always emit, got %d events", len(emitted))
	}
	if last := emitted[len(emitted)-1]; last.Status != "" || last.Objective != "" {
		t.Fatalf("cleared snapshot = %+v, want empty status/objective", last)
	}
}

// WithMode 后 goalHolder 的 onChange 必须重接到新 Agent:事件的 Turn 取自新
// agent 的 loopState,而不是冻结在第一个 Agent 上。
func TestWithModeRewiresGoalOnChange(t *testing.T) {
	client := llmtest.Client(llmtest.Turn(llmtest.Done(models.AssistantMessage("ok"), nil)))
	ag := New(Config{
		SystemPrompt: "x",
		Model:        models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		Mode:         "code",
	}, client, testRegistry("."), permissions.NewEngine(permissions.DefaultConfig()), events.New())

	var turns []int
	ag.Subscribe(func(_ context.Context, ev events.Event) error {
		if g, ok := ev.(events.GoalUpdatedEvent); ok {
			turns = append(turns, g.Turn)
		}
		return nil
	})

	ag.StartGoal("first", 0, 0) // old agent, turn 0

	fresh := ag.WithMode("review").(*Agent)
	fresh.loopState.SetTurn(42)
	fresh.StartGoal("second", 0, 0)

	if len(turns) != 2 {
		t.Fatalf("expected 2 goal events, got %v", turns)
	}
	if turns[1] != 42 {
		t.Fatalf("post-WithMode event Turn = %d, want 42 (fresh agent's loopState)", turns[1])
	}
}
