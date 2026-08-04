package agent

import (
	"context"
	"reflect"
	"testing"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/observability"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/task"
	"github.com/lcoder/lcoder/pkg/tools"
)

func testContextManager() *contextmgr.Manager {
	return contextmgr.NewManager(
		contextmgr.TokenBudget{
			MaxTotal:      128000,
			TargetTotal:   120000,
			ReserveOutput: 8192,
			MaxOutput:     16384,
		},
		contextmgr.WithSummarizer(func(_ context.Context, msgs []models.AgentMessage, _ string) (string, error) {
			return "summary", nil
		}),
	)
}

func TestAgentCheckpointRoundTrip(t *testing.T) {
	client := llmtest.Client(llmtest.Turn(llmtest.Done(models.AssistantMessage("ok"), nil)))
	registry := tools.NewRegistry(".")
	bus := events.New()
	obs := observability.NewCollector(observability.NewMemoryExporter())

	originalMgr := testContextManager()
	originalMgr.SetSystemPrompt("original prompt")

	original, err := NewBuilder().
		WithGatewayClient(client).
		WithRegistry(registry).
		WithEventBus(bus).
		WithPermissions(permissions.NewEngineFromRules(nil)).
		WithContextManager(originalMgr).
		WithModel("openai", "gpt-4o-mini").
		WithMode("review", NewModeManager()).
		WithObservability(obs).
		Build()
	if err != nil {
		t.Fatalf("build original agent: %v", err)
	}

	original.cfg.Mode = "review"
	_, _, _ = original.taskMgr.ReplaceAll([]task.Task{
		{Text: "step one", Status: task.StatusDone},
		{Text: "step two", Status: task.StatusInProgress},
	})
	steerMsg := models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "steer me"})
	original.Steer(steerMsg)
	original.executor.activateDeferredTool("read")

	cp, err := original.Checkpoint()
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	restoredMgr := testContextManager()
	restoredMgr.SetSystemPrompt("fresh prompt")

	restored, err := NewBuilder().
		WithGatewayClient(client).
		WithRegistry(registry).
		WithEventBus(events.New()).
		WithPermissions(permissions.NewEngineFromRules(nil)).
		WithContextManager(restoredMgr).
		WithModel("anthropic", "claude-sonnet").
		WithMode("code", NewModeManager()).
		Build()
	if err != nil {
		t.Fatalf("build restored agent: %v", err)
	}

	if err := restored.Restore(cp); err != nil {
		t.Fatalf("restore: %v", err)
	}

	if restored.cfg.Mode != "review" {
		t.Errorf("mode = %q, want %q", restored.cfg.Mode, "review")
	}
	wantModel := models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"}
	if restored.cfg.Model != wantModel {
		t.Errorf("model = %+v, want %+v", restored.cfg.Model, wantModel)
	}

	if len(restored.loopState.steeringQueue) != 1 || !reflect.DeepEqual(restored.loopState.steeringQueue, []models.AgentMessage{steerMsg}) {
		t.Errorf("steering queue = %+v, want %+v", restored.loopState.steeringQueue, []models.AgentMessage{steerMsg})
	}

	if !reflect.DeepEqual(restored.executor.activeDeferred, map[string]bool{"read": true}) {
		t.Errorf("active deferred = %+v, want %+v", restored.executor.activeDeferred, map[string]bool{"read": true})
	}

	// Verify task manager state is restored.
	if len(restored.taskMgr.List()) != 2 {
		t.Errorf("restored tasks = %+v, want 2 tasks", restored.taskMgr.List())
	}

	if b, ok := restored.mgr.GetBlock(contextmgr.BlockSystem, "system"); !ok {
		t.Error("restored manager missing system block")
	} else if b.Text() != "fresh prompt" {
		t.Errorf("system prompt = %q, want %q (checkpoint must not overwrite session messages)", b.Text(), "fresh prompt")
	}

	// Verify that block metadata (priority/cache hint) is restored.
	if b, ok := restored.mgr.GetBlock(contextmgr.BlockSystem, "system"); ok {
		if b.Priority != 100 {
			t.Errorf("system block priority = %d, want 100", b.Priority)
		}
	}
}

// restore 的 GoalUpdatedEvent 在 mgr/loopState 恢复之后才发射:事件携带恢复
// 后的 Turn,订阅者看到的是恢复完成的上下文。
func TestRestoreGoalEventCarriesRestoredTurn(t *testing.T) {
	client := llmtest.Client(llmtest.Turn(llmtest.Done(models.AssistantMessage("ok"), nil)))

	original := New(Config{
		SystemPrompt:   "x",
		Model:          models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ContextManager: testContextManager(),
	}, client, testRegistry("."), permissions.NewEngine(permissions.DefaultConfig()), events.New())
	original.StartGoal("ship it", 5, 0)
	original.loopState.SetTurn(9)

	cp, err := original.Checkpoint()
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	restored := New(Config{
		SystemPrompt:   "y",
		Model:          models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ContextManager: testContextManager(),
	}, client, testRegistry("."), permissions.NewEngine(permissions.DefaultConfig()), events.New())

	var goalEvents []events.GoalUpdatedEvent
	restored.Subscribe(func(_ context.Context, ev events.Event) error {
		if g, ok := ev.(events.GoalUpdatedEvent); ok {
			goalEvents = append(goalEvents, g)
		}
		return nil
	})

	if err := restored.Restore(cp); err != nil {
		t.Fatalf("restore: %v", err)
	}

	if len(goalEvents) != 1 {
		t.Fatalf("expected exactly 1 GoalUpdatedEvent from restore, got %d", len(goalEvents))
	}
	ev := goalEvents[0]
	if ev.Turn != 9 {
		t.Errorf("GoalUpdatedEvent.Turn = %d, want 9 (restored turn)", ev.Turn)
	}
	// A checkpointed active goal degrades to paused.
	if ev.Status != string(GoalPaused) {
		t.Errorf("GoalUpdatedEvent.Status = %q, want %q", ev.Status, GoalPaused)
	}
	if g := restored.Goal(); g == nil || g.Status != GoalPaused || g.Objective != "ship it" {
		t.Errorf("restored goal = %+v, want paused 'ship it'", g)
	}
}
