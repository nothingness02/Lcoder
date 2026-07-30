//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/checkpoint"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/tools"
)

// TestAgentCrashCheckpointResume verifies that a crash checkpoint written after
// a completed turn can be restored into a fresh agent. The session message
// history provides the conversation; the checkpoint provides runtime state such
// as turn counter and promoted deferred tools.
func TestAgentCrashCheckpointResume(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := checkpoint.NewFileStore(dir)
	sessionID := "crash-test"

	// First agent runs one turn: the assistant calls tool_search("edit"), which
	// promotes the deferred "edit" tool into the active set.
	client1 := llmtest.Client(llmtest.Turn(
		llmtest.Done(models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
			ID:        "ts1",
			Name:      "tool_search",
			Arguments: map[string]any{"query": "edit"},
		}), nil),
	))
	ag1 := buildCheckpointTestAgent(t, client1, dir, store, sessionID, func(context.Context, agent.TurnSummary) (bool, error) {
		// Stop after the first completed turn to simulate a crash boundary.
		return true, nil
	})

	if err := ag1.Prompt(ctx, models.UserMessage("find the edit tool")); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Simulate a crash by writing a crash checkpoint after the first turn.
	cp, err := ag1.CheckpointWithReason(checkpoint.ReasonCrash)
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := store.Save(sessionID, cp); err != nil {
		t.Fatalf("save crash checkpoint: %v", err)
	}

	savedMessages := ag1.AllMessages()
	if len(savedMessages) == 0 {
		t.Fatal("expected messages after first run")
	}

	// Build a fresh agent, load the session messages, restore the crash
	// checkpoint, and continue. The resumed turn should see the promoted "edit"
	// tool with its full schema instead of a deferred stub.
	client2, adapter2 := llmtest.NewScript(llmtest.Turn(llmtest.Done(models.AssistantMessage("all done"), nil)))
	ag2 := buildCheckpointTestAgent(t, client2, dir, store, sessionID, nil)

	ag2.SetMessages(savedMessages)
	loaded, err := store.Load(sessionID)
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if err := ag2.Restore(loaded); err != nil {
		t.Fatalf("restore: %v", err)
	}

	if err := ag2.Continue(ctx); err != nil {
		t.Fatalf("continue: %v", err)
	}

	if adapter2.CallCount() != 1 {
		t.Fatalf("expected 1 turn after restore, got %d", adapter2.CallCount())
	}

	lastReq := adapter2.LastRequest()
	editTool := findTool(lastReq.Tools, "edit")
	if editTool == nil {
		t.Fatalf("edit tool not present in resumed turn; tools=%v", toolNames(lastReq.Tools))
	}
	if len(editTool.Parameters) == 0 {
		t.Errorf("edit tool is still a deferred stub after restore; parameters=%+v", editTool.Parameters)
	}
	if strings.HasPrefix(editTool.Description, "(deferred)") {
		t.Errorf("edit tool description is still stubbed: %q", editTool.Description)
	}

	// The resumed turn should include the prior conversation (user prompt,
	// assistant tool_search call, and tool_search result), not just the user
	// prompt loaded from the session.
	if len(lastReq.Messages) < 3 {
		t.Errorf("expected resumed conversation to include user + assistant + tool_result messages, got %d", len(lastReq.Messages))
	}
}

func buildCheckpointTestAgent(t *testing.T, client *llm.Client, root string, store checkpoint.Store, sessionID string, shouldStop agent.ShouldStopFunc) *agent.Agent {
	t.Helper()

	mgr := contextmgr.NewManager(
		contextmgr.TokenBudget{MaxTotal: 4000, TargetTotal: 3000, ReserveOutput: 512},
		contextmgr.WithSummarizer(func(_ context.Context, _ []models.AgentMessage, _ string) (string, error) { return "summary", nil }),
	)
	mgr.SetSystemPrompt("test system prompt")

	registry := tools.NewRegistry(root)
	if err := registry.RegisterBuiltinFactories(root); err != nil {
		t.Fatalf("register builtin tools: %v", err)
	}

	cfg := agent.Config{
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-test"},
		ContextManager:    mgr,
		Mode:              "code",
		DeferredTools:     true,
		CoreTools:         []string{"read", "ls"},
		SessionID:         sessionID,
		CheckpointStore:   store,
		ShouldStop:        shouldStop,
	}

	ag, err := agent.NewBuilder().
		WithConfig(cfg).
		WithGatewayClient(client).
		WithRegistry(registry).
		WithPermissions(permissions.NewEngineFromRules(nil)).
		WithEventBus(events.New()).
		Build()
	if err != nil {
		t.Fatalf("build agent: %v", err)
	}
	return ag
}

func findTool(tools []models.ToolDefinition, name string) *models.ToolDefinition {
	for i := range tools {
		if tools[i].Name == name {
			return &tools[i]
		}
	}
	return nil
}

func toolNames(tools []models.ToolDefinition) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	return names
}
