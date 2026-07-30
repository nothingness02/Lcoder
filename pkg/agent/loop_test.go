package agent

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/observability"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/tools"
	"github.com/lcoder/lcoder/pkg/tools/builtin"
)

func testRegistry(root string) *tools.Registry {
	registry := tools.NewRegistry(root)
	for _, f := range builtin.Factories() {
		registry.RegisterBuiltin(f)
	}
	return registry
}

// abortTestToolStarted is closed by blockingToolExec when it starts, so tests
// can wait for the tool to be in-flight before calling Abort.
var abortTestToolStarted = make(chan struct{}, 1)

func TestAgentSessionID(t *testing.T) {
	ag := New(Config{SessionID: "initial"}, nil, nil, nil, nil)
	if ag.SessionID() != "initial" {
		t.Fatalf("SessionID() = %q, want %q", ag.SessionID(), "initial")
	}
	ag.SetSessionID("switched")
	if ag.SessionID() != "switched" {
		t.Fatalf("SetSessionID did not update session id: got %q", ag.SessionID())
	}

	// Checkpoints created after the switch must carry the new session id.
	cp, err := ag.Checkpoint()
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if cp.Session.SessionID != "switched" {
		t.Fatalf("checkpoint session id = %q, want %q", cp.Session.SessionID, "switched")
	}
}

func TestAgentOneTurn(t *testing.T) {
	client := llmtest.Client(llmtest.Turn(
		llmtest.Start(),
		llmtest.Text("Hello"),
		llmtest.Done(models.AssistantMessage("Hello"), nil),
	))

	bus := events.New()
	var got []string
	bus.Subscribe(func(ctx context.Context, ev events.Event) error {
		got = append(got, string(ev.EventType()))
		return nil
	})

	obs := observability.NewCollector(observability.NewMemoryExporter())
	ag := NewWithObservability(Config{
		SystemPrompt:      "You are helpful.",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionParallel,
	}, client, testRegistry("."), permissions.NewEngine(permissions.DefaultConfig()), bus, obs)

	if err := ag.Prompt(context.Background(), models.UserMessage("hi")); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	if len(got) < 2 {
		t.Fatalf("expected events, got %v", got)
	}
	if got[0] != "agent_start" {
		t.Fatalf("expected agent_start, got %v", got)
	}
	if got[len(got)-1] != "agent_end" {
		t.Fatalf("expected agent_end last, got %v", got)
	}
}

func TestAgentToolCall(t *testing.T) {
	// First turn requests a tool; the finalized done message carries the call.
	toolMsg := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Type: "tool_call", ID: "call_1", Name: "ls", Arguments: map[string]any{},
	})
	client := llmtest.Client(llmtest.Turn(llmtest.Done(toolMsg, nil)))

	bus := events.New()
	var got []string
	bus.Subscribe(func(ctx context.Context, ev events.Event) error {
		got = append(got, string(ev.EventType()))
		return nil
	})

	obs := observability.NewCollector(observability.NewMemoryExporter())
	ag := NewWithObservability(Config{
		SystemPrompt:      "You are helpful.",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionParallel,
		ShouldStop: func(ctx context.Context, turn TurnSummary) (bool, error) {
			return true, nil
		},
	}, client, testRegistry(t.TempDir()), permissions.NewEngine(permissions.DefaultConfig()), bus, obs)

	if err := ag.Prompt(context.Background(), models.UserMessage("list files")); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	var hasToolStart, hasToolEnd bool
	for _, e := range got {
		if e == "tool_execution_start" {
			hasToolStart = true
		}
		if e == "tool_execution_end" {
			hasToolEnd = true
		}
	}
	if !hasToolStart || !hasToolEnd {
		t.Fatalf("expected tool events, got %v", got)
	}
}

func TestAgentNaturalCompletionDefault(t *testing.T) {
	// With no ShouldStop configured, the loop must default to "natural
	// completion": keep streaming while the model is still calling tools, and
	// stop on the first turn that produces a plain-text answer (no tool calls).
	toolMsg := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Type: "tool_call", ID: "call_1", Name: "ls", Arguments: map[string]any{},
	})
	client, adapter := llmtest.NewScript(
		llmtest.Turn(llmtest.Done(toolMsg, nil)),                             // turn 0: a tool call
		llmtest.Turn(llmtest.Done(models.AssistantMessage("all done"), nil)), // turn 1: final answer
	)

	obs := observability.NewCollector(observability.NewMemoryExporter())
	ag := NewWithObservability(Config{
		SystemPrompt:      "You are helpful.",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionParallel,
		// Deliberately no ShouldStop: exercise the default behavior.
	}, client, testRegistry(t.TempDir()), permissions.NewEngine(permissions.DefaultConfig()), events.New(), obs)

	if err := ag.Prompt(context.Background(), models.UserMessage("list files")); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	// Exactly two streamed turns: the tool call, then the final answer the model
	// produced after seeing the tool result.
	if adapter.CallCount() != 2 {
		t.Fatalf("expected 2 turns (tool call then final answer), got %d", adapter.CallCount())
	}
}

func TestAgentAbortIsReentrant(t *testing.T) {
	// A slow stream keeps the turn in-flight while Abort is called repeatedly.
	client, adapter := llmtest.NewScript(llmtest.Turn(
		append(llmtest.RepeatText("x", 100), llmtest.Done(models.AssistantMessage("done"), nil))...,
	))
	adapter.Delay = time.Millisecond

	obs := observability.NewCollector(observability.NewMemoryExporter())
	ag := NewWithObservability(Config{
		SystemPrompt:      "You are helpful.",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionParallel,
	}, client, testRegistry("."), permissions.NewEngine(permissions.DefaultConfig()), events.New(), obs)

	// Calling Abort multiple times must not panic.
	go func() {
		ag.Abort()
		ag.Abort()
		ag.Abort()
	}()

	_ = ag.Prompt(context.Background(), models.UserMessage("hi"))
}

func TestAgentAbortStopsStream(t *testing.T) {
	client, adapter := llmtest.NewScript(llmtest.Turn(
		append(llmtest.RepeatText("x", 50), llmtest.Done(models.AssistantMessage("long"), nil))...,
	))
	adapter.Delay = 5 * time.Millisecond

	obs := observability.NewCollector(observability.NewMemoryExporter())
	ag := NewWithObservability(Config{
		SystemPrompt:      "You are helpful.",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionParallel,
	}, client, testRegistry("."), permissions.NewEngine(permissions.DefaultConfig()), events.New(), obs)

	go func() {
		time.Sleep(25 * time.Millisecond)
		ag.Abort()
	}()

	err := ag.Prompt(context.Background(), models.UserMessage("hi"))
	// Aborting mid-stream returns context.Canceled. If the abort happens
	// before the stream starts, the full turn may complete. Both are valid
	// as long as the call does not panic.
	if err != nil {
		return
	}
	msgs := ag.AllMessages()
	if len(msgs) == 0 {
		t.Fatal("expected some messages after aborted prompt")
	}
	last := msgs[len(msgs)-1]
	if last.Role == models.RoleAssistant {
		// If the full turn completed, the assistant message should be short
		// (not the 50-delta "long" final message).
		if last.Text() == "long" {
			t.Fatal("expected stream to abort before producing final long message")
		}
	}
}

func TestAgentWithModeSnapshot(t *testing.T) {
	client := llmtest.Client(llmtest.Turn(llmtest.Done(models.AssistantMessage("ok"), nil)))

	mm := NewModeManager()
	mm.modes["code"] = ModeConfig{Name: "code", SystemPrompt: "code mode"}
	mm.modes["review"] = ModeConfig{Name: "review", SystemPrompt: "review mode"}

	mgr := contextmgr.NewManager(
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

	obs := observability.NewCollector(observability.NewMemoryExporter())
	ag := NewWithObservability(Config{
		SystemPrompt:      "base",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionParallel,
		ModeManager:       mm,
		Mode:              "code",
		ContextManager:    mgr,
	}, client, testRegistry("."), permissions.NewEngine(permissions.DefaultConfig()), events.New(), obs)

	steerMsg := models.UserMessage("steer me")
	ag.Steer(steerMsg)
	ag.executor.activateDeferredTool("edit")

	reviewAg := ag.WithMode("review").(*Agent)
	if reviewAg.cfg.Mode != "review" {
		t.Fatalf("expected review mode, got %s", reviewAg.cfg.Mode)
	}
	// The snapshot should share the same base context system prompt, not
	// have accumulated mode prompts from the original agent.
	if reviewAg.mgr.SystemPrompt() != ag.mgr.SystemPrompt() {
		t.Fatalf("WithMode should snapshot base system prompt")
	}
	if len(reviewAg.loopState.steeringQueue) != 1 || !reflect.DeepEqual(reviewAg.loopState.steeringQueue, []models.AgentMessage{steerMsg}) {
		t.Errorf("steering queue = %+v, want %+v", reviewAg.loopState.steeringQueue, []models.AgentMessage{steerMsg})
	}
	if !reflect.DeepEqual(reviewAg.executor.activeDeferred, map[string]bool{"edit": true}) {
		t.Errorf("active deferred = %+v, want %+v", reviewAg.executor.activeDeferred, map[string]bool{"edit": true})
	}
	// The original agent should be untouched.
	if len(ag.loopState.steeringQueue) != 1 || !reflect.DeepEqual(ag.loopState.steeringQueue, []models.AgentMessage{steerMsg}) {
		t.Errorf("original steering queue mutated: %+v", ag.loopState.steeringQueue)
	}
	if reviewAg.mgr == ag.mgr {
		t.Error("WithMode should create an independent context manager")
	}
}

func TestAgentTransformContext(t *testing.T) {
	client, adapter := llmtest.NewScript(llmtest.Turn(
		llmtest.Done(models.AssistantMessage("ok"), nil),
	))

	transformCalled := false
	obs := observability.NewCollector(observability.NewMemoryExporter())
	ag := NewWithObservability(Config{
		SystemPrompt:      "You are helpful.",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionParallel,
		TransformContext: func(ctx context.Context, msgs []models.AgentMessage) ([]models.AgentMessage, error) {
			transformCalled = true
			return msgs, nil
		},
	}, client, testRegistry("."), permissions.NewEngine(permissions.DefaultConfig()), events.New(), obs)

	_ = ag.Prompt(context.Background(), models.UserMessage("hi"))
	if !transformCalled {
		t.Fatal("expected TransformContext to be called")
	}
	// After the identity transform the request carries the compacted messages.
	if n := len(adapter.LastRequest().Messages); n > 3 {
		t.Fatalf("expected transformed messages, got %d", n)
	}
}

type userConfirmFunc func(ctx context.Context, info ToolCallInfo) (bool, error)

func (f userConfirmFunc) Confirm(ctx context.Context, info ToolCallInfo) (bool, error) {
	return f(ctx, info)
}

func (f userConfirmFunc) ConfirmWithScope(ctx context.Context, info ToolCallInfo) (ConfirmResult, error) {
	allow, err := f(ctx, info)
	return ConfirmResult{Allow: allow, Scope: ScopeOnce}, err
}

func TestAgentPermissionAskAllowed(t *testing.T) {
	toolMsg := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Type: "tool_call", ID: "call_1", Name: "ls", Arguments: map[string]any{},
	})
	client := llmtest.Client(llmtest.Turn(llmtest.Done(toolMsg, nil)))

	bus := events.New()
	var audit []events.AuditEvent
	bus.Subscribe(func(ctx context.Context, ev events.Event) error {
		if a, ok := ev.(events.AuditEvent); ok {
			audit = append(audit, a)
		}
		return nil
	})

	perms := permissions.NewEngineFromRules([]permissions.Rule{
		{Tool: "ls", Pattern: "*", Decision: permissions.Ask},
	})
	confirmed := false
	ag := New(Config{
		SystemPrompt:      "You are helpful.",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionParallel,
		ShouldStop: func(ctx context.Context, turn TurnSummary) (bool, error) {
			return true, nil
		},
	}, client, testRegistry(t.TempDir()), perms, bus)
	ag.SetUserConfirm(userConfirmFunc(func(ctx context.Context, info ToolCallInfo) (bool, error) {
		confirmed = true
		return true, nil
	}))

	if err := ag.Prompt(context.Background(), models.UserMessage("list files")); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if !confirmed {
		t.Fatal("expected user confirmation to be requested")
	}
	var sawToolEnd bool
	for _, ev := range audit {
		if ev.Decision == "ask" {
			sawToolEnd = true
		}
	}
	if !sawToolEnd {
		t.Fatalf("expected ask audit event, got %+v", audit)
	}
}

func TestAgentPermissionDeny(t *testing.T) {
	toolMsg := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Type: "tool_call", ID: "call_1", Name: "ls", Arguments: map[string]any{},
	})
	client, adapter := llmtest.NewScript(
		llmtest.Turn(llmtest.Done(toolMsg, nil)),
		llmtest.Turn(llmtest.Done(models.AssistantMessage("blocked"), nil)),
	)

	perms := permissions.NewEngineFromRules([]permissions.Rule{
		{Tool: "ls", Pattern: "*", Decision: permissions.Deny},
	})
	ag := New(Config{
		SystemPrompt:      "You are helpful.",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionParallel,
	}, client, testRegistry(t.TempDir()), perms, events.New())

	if err := ag.Prompt(context.Background(), models.UserMessage("list files")); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if adapter.CallCount() != 2 {
		t.Fatalf("expected 2 turns after denied tool, got %d", adapter.CallCount())
	}
}

func TestAgentEventHandlerErrorIsObservedNotFatal(t *testing.T) {
	client := llmtest.Client(llmtest.Turn(
		llmtest.Done(models.AssistantMessage("ok"), nil),
	))

	bus := events.New()
	bus.Subscribe(func(ctx context.Context, ev events.Event) error {
		if ev.EventType() == events.MessageStart {
			return fmt.Errorf("intentional handler failure")
		}
		return nil
	})

	exporter := observability.NewMemoryExporter()
	obs := observability.NewCollector(exporter)
	_ = obs.Subscribe(bus)
	ag := NewWithObservability(Config{
		SystemPrompt:      "You are helpful.",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionParallel,
	}, client, testRegistry("."), permissions.NewEngine(permissions.DefaultConfig()), bus, obs)

	if err := ag.Prompt(context.Background(), models.UserMessage("hi")); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	_ = bus.Close()

	var found bool
	for _, r := range exporter.Records {
		if r.Type == "span_end" && r.Span != nil && r.Span.Name == "agent_run" {
			for _, e := range r.Span.Events {
				if e.Name == "runtime_error" {
					found = true
					break
				}
			}
		}
	}
	if !found {
		t.Fatalf("expected runtime_error event on agent span, got records %+v", exporter.Records)
	}
}

func TestAgentTransformContextPreservesEphemeralReminders(t *testing.T) {
	client, adapter := llmtest.NewScript(llmtest.Turn(
		llmtest.Done(models.AssistantMessage("ok"), nil),
	))

	ag := New(Config{
		SystemPrompt:      "You are helpful.",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionParallel,
		ReminderProducers: []ReminderProducer{
			func(messages []models.AgentMessage) []string {
				return []string{"remember to check tests"}
			},
		},
		TransformContext: func(ctx context.Context, msgs []models.AgentMessage) ([]models.AgentMessage, error) {
			// Drop the first user message but keep everything else.
			if len(msgs) > 1 {
				return msgs[1:], nil
			}
			return msgs, nil
		},
	}, client, testRegistry("."), permissions.NewEngine(permissions.DefaultConfig()), events.New())

	_ = ag.Prompt(context.Background(), models.UserMessage("hi"))

	req := adapter.LastRequest()
	found := false
	for _, m := range req.Messages {
		if m.Metadata != nil && m.Metadata["ephemeral"] == true {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected ephemeral reminder to survive TransformContext path, got messages %+v", req.Messages)
	}
}

func TestAgentTransformContextPreservesCacheBreakpoints(t *testing.T) {
	client, adapter := llmtest.NewScript(llmtest.Turn(
		llmtest.Done(models.AssistantMessage("ok"), nil),
	))

	ag := New(Config{
		SystemPrompt:      "You are helpful.",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionParallel,
		TransformContext: func(ctx context.Context, msgs []models.AgentMessage) ([]models.AgentMessage, error) {
			return msgs, nil
		},
	}, client, testRegistry("."), permissions.NewEngine(permissions.DefaultConfig()), events.New())

	_ = ag.Prompt(context.Background(), models.UserMessage("hi"))

	req := adapter.LastRequest()
	if len(req.CacheBreakpoints) == 0 {
		t.Fatal("expected cache breakpoints to be preserved through identity TransformContext")
	}
}

func TestAgentTransformContextRecomputesBreakpointsWhenCountChanges(t *testing.T) {
	client, adapter := llmtest.NewScript(llmtest.Turn(
		llmtest.Done(models.AssistantMessage("ok"), nil),
	))

	ag := New(Config{
		SystemPrompt:      "You are helpful.",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionParallel,
		TransformContext: func(ctx context.Context, msgs []models.AgentMessage) ([]models.AgentMessage, error) {
			return msgs[:1], nil
		},
	}, client, testRegistry("."), permissions.NewEngine(permissions.DefaultConfig()), events.New())

	_ = ag.Prompt(context.Background(), models.UserMessage("hi"))

	req := adapter.LastRequest()
	if len(req.Messages) != 1 {
		t.Fatalf("expected 1 transformed message, got %d", len(req.Messages))
	}
	if len(req.CacheBreakpoints) == 0 {
		t.Fatal("expected cache breakpoints to be recomputed after message count changed")
	}
	if req.CacheBreakpoints[0] != 0 {
		t.Fatalf("expected recomputed breakpoint at index 0, got %v", req.CacheBreakpoints)
	}
}

func TestAgentTransformContextRecomputesMaxTokens(t *testing.T) {
	client, adapter := llmtest.NewScript(llmtest.Turn(
		llmtest.Done(models.AssistantMessage("ok"), nil),
	))

	ag := New(Config{
		SystemPrompt:      "You are helpful.",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionParallel,
		TransformContext: func(ctx context.Context, msgs []models.AgentMessage) ([]models.AgentMessage, error) {
			return msgs[:1], nil
		},
	}, client, testRegistry("."), permissions.NewEngine(permissions.DefaultConfig()), events.New())

	_ = ag.Prompt(context.Background(), models.UserMessage("hi this is a long prompt to make max_tokens differ from a hardcoded value"))

	req := adapter.LastRequest()
	if req.Generation.MaxTokens <= 0 {
		t.Fatalf("expected MaxTokens to be recomputed, got %d", req.Generation.MaxTokens)
	}
}

// blockingToolExec blocks until its context is canceled, letting us verify that
// Agent.Abort() cancels the whole run, not just the LLM stream.
type blockingToolExec struct{}

func (blockingToolExec) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name:        "blocker",
		Description: "Blocks until aborted.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func (blockingToolExec) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	// This global signal is ugly but sufficient for the single-test scenario.
	abortTestToolStarted <- struct{}{}
	<-ctx.Done()
	return models.NewToolExecutionResultError("aborted by cancel"), nil
}

func TestAgentAbortCancelsRunningTool(t *testing.T) {
	abortTestToolStarted = make(chan struct{})

	registry := tools.NewRegistry(t.TempDir())
	registry.Register("blocker", blockingToolExec{})

	toolCallMsg := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Type: "tool_call", ID: "call_1", Name: "blocker", Arguments: map[string]any{},
	})
	client := llmtest.Client(llmtest.Turn(llmtest.Done(toolCallMsg, nil)))

	obs := observability.NewCollector(observability.NewMemoryExporter())
	ag := NewWithObservability(Config{
		SystemPrompt:      "You are helpful.",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionParallel,
	}, client, registry, permissions.NewEngine(permissions.DefaultConfig()), events.New(), obs)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- ag.Prompt(ctx, models.UserMessage("run blocker"))
	}()

	select {
	case <-abortTestToolStarted:
		// Tool started; abort the run.
		ag.Abort()
	case <-time.After(2 * time.Second):
		t.Fatal("tool did not start")
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected clean return after abort, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt did not return after Abort")
	}
}

func TestShouldContinueAfterStopHook(t *testing.T) {
	// One-turn agent: the LLM returns a single message with no tool calls.
	// The default ShouldStop stops there. We wire ShouldContinueAfterStop
	// to override the stop decision.
	client := llmtest.Client(llmtest.Turn(llmtest.Done(models.NewAgentMessage(
		models.RoleAssistant,
		models.TextContent{Text: "done"},
	), nil)))

	hookCalled := false
	ag := New(Config{
		SystemPrompt: "x",
		Model:        models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ShouldContinueAfterStop: func(ctx context.Context, turn TurnSummary) (bool, error) {
			hookCalled = true
			return false, nil
		},
	}, client, testRegistry("."), permissions.NewEngine(permissions.DefaultConfig()), events.New())

	if err := ag.Prompt(context.Background(), models.UserMessage("go")); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if !hookCalled {
		t.Fatal("ShouldContinueAfterStop hook was not called")
	}

	// hook was called and loop terminated correctly.
	msgs := ag.AllMessages()
	if len(msgs) < 2 {
		t.Fatalf("expected at least 2 messages (user+asst), got %d: %v", len(msgs), msgs)
	}
	// Verify the last message is the assistant's "done" response.
	last := msgs[len(msgs)-1]
	if last.Role != models.RoleAssistant || last.Text() != "done" {
		t.Fatalf("expected assistant 'done', got role=%v text=%q", last.Role, last.Text())
	}
}
