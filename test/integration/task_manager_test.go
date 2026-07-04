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
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/llm/provider"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/task"
	"github.com/lcoder/lcoder/pkg/tools"
)

// TestTaskManagerSurvivesCompactionAndReconcilesOverwrites exercises the six
// task-system concerns raised in the TaskManager review:
//
//   1. After context compaction the agent still recognizes unfinished tasks.
//   2. The agent perceives unfinished tasks via an ephemeral reminder each turn.
//   3. Task lists are updated through todo_write and reconciled against the
//      protected TaskManager state.
//   4. Crash checkpoint restore recovers the exact task state, not just the
//      (possibly compacted away) message history.
//   5. New lists replace old lists but unfinished tasks are auto-recovered.
//   6. A model cannot silently overwrite unfinished tasks and bias the plan.
func TestTaskManagerSurvivesCompactionAndReconcilesOverwrites(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := checkpoint.NewFileStore(dir)
	sessionID := "task-manager-integration"

	// Deterministic summarizer keeps the test hermetic (no API key needed).
	summarizer := func([]models.AgentMessage) (string, error) {
		return "summary of earlier conversation", nil
	}
	mgr := contextmgr.NewManager(
		contextmgr.TokenBudget{MaxTotal: 500, TargetTotal: 400, ReserveOutput: 50},
		contextmgr.WithSummarizer(summarizer),
		contextmgr.WithMinRecent(2),
	)
	mgr.SetSystemPrompt("test system prompt")

	registry := tools.NewRegistry(".")
	if err := registry.RegisterBuiltinFactories("."); err != nil {
		t.Fatalf("register builtin tools: %v", err)
	}

	bus := events.New()
	var taskListEvents [][]task.Task
	bus.Subscribe(func(_ context.Context, ev events.Event) error {
		if e, ok := ev.(events.TaskListUpdatedEvent); ok {
			taskListEvents = append(taskListEvents, e.Tasks)
		}
		return nil
	})

	longFiller := strings.Repeat("x", 2200) // ~550 tokens under DefaultEstimator.

	longSteer := models.UserMessage(strings.Repeat("y", 2000)) // ~500 tokens; acts as a recent user message so compaction can fold older history.

	// Turn 0: the model declares three pending tasks.
	// Turn 1: normal progress.
	// Turn 2: overwrite attempt — drops unfinished tasks B and C.
	// Turn 3: large filler message to push context over the compaction threshold
	//         and then advance the task list.
	// Turn 4: final text answer (no tool calls) so the default stop heuristic ends
	//         the run; the reminder for the still-unfinished task C must still be
	//         injected into this last request.
	script := [][]provider.Event{
		llmtest.Turn(llmtest.Done(todoCallWithText("c0", []task.Task{
			{Text: "task A", Status: task.StatusPending},
			{Text: "task B", Status: task.StatusPending},
			{Text: "task C", Status: task.StatusPending},
		}, ""), nil)),
		llmtest.Turn(llmtest.Done(todoCallWithText("c1", []task.Task{
			{Text: "task A", Status: task.StatusDone},
			{Text: "task B", Status: task.StatusInProgress},
			{Text: "task C", Status: task.StatusPending},
		}, ""), nil)),
		llmtest.Turn(llmtest.Done(todoCallWithText("c2", []task.Task{
			{Text: "task A", Status: task.StatusDone},
		}, ""), nil)),
		llmtest.Turn(llmtest.Done(todoCallWithText("c3", []task.Task{
			{Text: "task A", Status: task.StatusDone},
			{Text: "task B", Status: task.StatusDone},
			{Text: "task C", Status: task.StatusInProgress},
		}, longFiller), nil)),
		llmtest.Turn(llmtest.Done(models.AssistantMessage("Done for now."), nil)),
	}
	client, adapter := llmtest.NewScript(script...)

	var ag *agent.Agent
	todoWritesSeen := 0
	stoppedOnce := false
	cfg := agent.Config{
		SystemPrompt:      "test system prompt",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-test"},
		MaxTurns:          5,
		ToolExecutionMode: models.ExecutionSequential,
		ContextManager:    mgr,
		SessionID:         sessionID,
		CheckpointStore:   store,
		ShouldStop: func(_ context.Context, ts agent.TurnSummary) (bool, error) {
			for _, tc := range ts.Message.ToolCalls() {
				if tc.Name == task.ToolName {
					todoWritesSeen++
					break
				}
			}
			if todoWritesSeen == 3 && !stoppedOnce {
				stoppedOnce = true
				ag.Steer(longSteer)
				return true, nil
			}
			return false, nil
		},
	}
	ag = agent.New(cfg, client, registry, permissions.NewEngineFromRules(nil), bus)

	if err := ag.Prompt(ctx, models.UserMessage("plan and track the tasks")); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	// --- Assertion 1: overwrite reconciliation ---
	// Turn 2 supplied only task A; TaskManager must have re-added B and C.
	if len(taskListEvents) < 3 {
		t.Fatalf("expected at least 3 task-list events, got %d", len(taskListEvents))
	}
	if got := len(taskListEvents[2]); got != 3 {
		t.Errorf("turn 2 reconciled task count = %d, want 3 (unfinished tasks recovered)", got)
	}

	// The tool result for the overwrite attempt should carry warnings.
	if !toolResultContains(ag.AllMessages(), "c2", "re-added unfinished task") {
		t.Errorf("turn 2 todo_write result should warn about re-added unfinished tasks")
	}

	// Continue into the post-compaction phase. The steer message injected by
	// ShouldStop becomes the recent user message that allows foldOlder to drop
	// the early todo_write calls without keeping the entire conversation.
	if err := ag.Continue(ctx); err != nil {
		t.Fatalf("continue: %v", err)
	}

	// --- Assertion 2: compaction happened ---
	var compacted bool
	for _, m := range ag.AllMessages() {
		if strings.Contains(m.Text(), "[Summary of earlier conversation]") {
			compacted = true
			break
		}
	}
	if !compacted {
		t.Fatal("expected context compaction to inject a summary message")
	}

	// --- Assertion 3: task state survived compaction and overwrites ---
	wantFinal := map[string]task.Status{
		"task A": task.StatusDone,
		"task B": task.StatusDone,
		"task C": task.StatusInProgress,
	}
	assertTaskMap(t, ag.TaskManager().List(), wantFinal)

	// --- Assertion 4: unfinished-task reminder is injected into the LLM request ---
	lastReq := adapter.LastRequest()
	if !requestHasReminderFor(lastReq, "unfinished todo item") {
		t.Errorf("final turn request should contain an ephemeral reminder for unfinished tasks")
	}

	// --- Assertion 5: checkpoint restore preserves task state ---
	cp, err := ag.CheckpointWithReason(checkpoint.ReasonManual)
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := store.Save(sessionID, cp); err != nil {
		t.Fatalf("save checkpoint: %v", err)
	}
	savedMessages := ag.AllMessages()

	// A fresh agent loads the compacted session, then restores the checkpoint.
	restoredMgr := contextmgr.NewManager(
		contextmgr.TokenBudget{MaxTotal: 500, TargetTotal: 400, ReserveOutput: 50},
		contextmgr.WithSummarizer(summarizer),
		contextmgr.WithMinRecent(2),
	)
	restoredMgr.SetSystemPrompt("test system prompt")
	restoredCfg := cfg
	restoredCfg.ContextManager = restoredMgr

	client2, _ := llmtest.NewScript(llmtest.Turn(llmtest.Done(todoCallWithText("c4", []task.Task{
		{Text: "task A", Status: task.StatusDone},
		{Text: "task B", Status: task.StatusDone},
		{Text: "task C", Status: task.StatusDone},
	}, ""), nil)))
	ag2 := agent.New(restoredCfg, client2, registry, permissions.NewEngineFromRules(nil), events.New())
	ag2.SetMessages(savedMessages)

	loaded, err := store.Load(sessionID)
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if err := ag2.Restore(loaded); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// Before continuing, the restored TaskManager must already hold the protected
	// state, even though the original todo_write calls are inside a compaction
	// summary in the message history.
	assertTaskMap(t, ag2.TaskManager().List(), wantFinal)

	if err := ag2.Continue(ctx); err != nil {
		t.Fatalf("continue: %v", err)
	}

	// After one more turn, task C is done.
	wantAfterRestore := map[string]task.Status{
		"task A": task.StatusDone,
		"task B": task.StatusDone,
		"task C": task.StatusDone,
	}
	assertTaskMap(t, ag2.TaskManager().List(), wantAfterRestore)
}

// todoCallWithText builds an assistant message that calls todo_write. If text is
// non-empty it is included as a preceding text content part, which lets tests
// inflate the context window to trigger compaction.
func todoCallWithText(callID string, todos []task.Task, text string) models.AgentMessage {
	items := make([]any, 0, len(todos))
	for _, td := range todos {
		items = append(items, map[string]any{
			"text":   td.Text,
			"status": string(td.Status),
		})
	}
	parts := make([]models.ContentPart, 0, 2)
	if text != "" {
		parts = append(parts, models.TextContent{Text: text})
	}
	parts = append(parts, models.ToolCallContent{
		Type:      "tool_call",
		ID:        callID,
		Name:      task.ToolName,
		Arguments: map[string]any{"todos": items},
	})
	return models.NewAgentMessage(models.RoleAssistant, parts...)
}

// requestHasReminderFor reports whether any ephemeral message in req mentions s.
func requestHasReminderFor(req models.TurnRequest, s string) bool {
	for _, m := range req.Messages {
		if contextmgr.IsEphemeral(m) && strings.Contains(m.Text(), s) {
			return true
		}
	}
	return false
}

// toolResultContains reports whether the tool_result message matching callID
// contains substr in its result content text.
func toolResultContains(msgs []models.AgentMessage, callID, substr string) bool {
	for _, m := range msgs {
		if len(m.Content) == 0 {
			continue
		}
		res, ok := m.Content[0].(models.ToolResultContent)
		if !ok {
			continue
		}
		if res.ToolCallID != callID {
			continue
		}
		for _, part := range res.Content {
			if t, ok := part.(models.TextContent); ok && strings.Contains(t.Text, substr) {
				return true
			}
		}
	}
	return false
}

// assertTaskMap verifies that tasks exactly match the want map.
func assertTaskMap(t *testing.T, tasks []task.Task, want map[string]task.Status) {
	t.Helper()
	if len(tasks) != len(want) {
		t.Fatalf("task count = %d, want %d; got %+v", len(tasks), len(want), tasks)
	}
	for _, td := range tasks {
		wantStatus, ok := want[td.Text]
		if !ok {
			t.Errorf("unexpected task %q", td.Text)
			continue
		}
		if td.Status != wantStatus {
			t.Errorf("task %q status = %q, want %q", td.Text, td.Status, wantStatus)
		}
	}
}
