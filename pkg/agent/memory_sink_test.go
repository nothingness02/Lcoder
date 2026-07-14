package agent

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/memory"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/tools"
)

type fakeMemorySink struct {
	prefetchQueries []string
	synced          [][2]string
	ended           []memory.SessionSummary
}

func (f *fakeMemorySink) Prefetch(ctx context.Context, query string) error {
	f.prefetchQueries = append(f.prefetchQueries, query)
	return nil
}

func (f *fakeMemorySink) SyncTurn(ctx context.Context, user, assistant string) error {
	f.synced = append(f.synced, [2]string{user, assistant})
	return nil
}

func (f *fakeMemorySink) OnSessionEnd(ctx context.Context, summary memory.SessionSummary) error {
	f.ended = append(f.ended, summary)
	return nil
}

func TestAgentCallsMemorySinkLifecycle(t *testing.T) {
	client := llmtest.Client(llmtest.Turn(
		llmtest.Done(models.AssistantMessage("hello back"), nil),
	))

	sink := &fakeMemorySink{}
	bus := events.New()
	registry := tools.NewRegistry(".")
	perms := permissions.NewEngine(permissions.DefaultConfig())

	cfg := Config{
		SystemPrompt:      "You are helpful.",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionParallel,
		MemoryInjector:    sink,
		SessionID:         "test-session",
		ShouldStop: func(ctx context.Context, turn TurnSummary) (bool, error) {
			return true, nil
		},
	}
	ag := New(cfg, client, registry, perms, bus)

	ctx := context.Background()
	if err := ag.Prompt(ctx, models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hello"})); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	if !slices.Contains(sink.prefetchQueries, "hello") {
		t.Errorf("expected prefetch query %q, got %v", "hello", sink.prefetchQueries)
	}
	if len(sink.synced) != 1 {
		t.Fatalf("expected 1 synced turn, got %d", len(sink.synced))
	}
	if sink.synced[0][0] != "hello" {
		t.Errorf("expected user text %q, got %q", "hello", sink.synced[0][0])
	}
	if sink.synced[0][1] != "hello back" {
		t.Errorf("expected assistant text %q, got %q", "hello back", sink.synced[0][1])
	}
	if len(sink.ended) != 1 {
		t.Fatalf("expected 1 session end, got %d", len(sink.ended))
	}
	if sink.ended[0].SessionID != "test-session" {
		t.Errorf("expected session id %q, got %q", "test-session", sink.ended[0].SessionID)
	}
	if sink.ended[0].TurnCount != 1 {
		t.Errorf("expected turn count 1, got %d", sink.ended[0].TurnCount)
	}
}

type plainInjector struct{ called bool }

func (p *plainInjector) Prefetch(ctx context.Context, query string) error {
	p.called = true
	return nil
}

func TestAgentCallsPrefetchForPlainInjector(t *testing.T) {
	client := llmtest.Client(llmtest.Turn(
		llmtest.Done(models.AssistantMessage("hi"), nil),
	))

	inj := &plainInjector{}
	bus := events.New()
	registry := tools.NewRegistry(".")
	perms := permissions.NewEngine(permissions.DefaultConfig())

	cfg := Config{
		SystemPrompt:      "You are helpful.",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionParallel,
		MemoryInjector:    inj,
		ShouldStop: func(ctx context.Context, turn TurnSummary) (bool, error) {
			return true, nil
		},
	}
	ag := New(cfg, client, registry, perms, bus)

	ctx := context.Background()
	if err := ag.Prompt(ctx, models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hello"})); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	if !inj.called {
		t.Error("plain injector Prefetch should be called")
	}
}

type errorSink struct {
	prefetchErr     error
	syncTurnErr     error
	onSessionEndErr error
}

func (e *errorSink) Prefetch(ctx context.Context, query string) error           { return e.prefetchErr }
func (e *errorSink) SyncTurn(ctx context.Context, user, assistant string) error { return e.syncTurnErr }
func (e *errorSink) OnSessionEnd(ctx context.Context, summary memory.SessionSummary) error {
	return e.onSessionEndErr
}

func TestAgentMemorySinkErrorsEmitErrorEvents(t *testing.T) {
	client := llmtest.Client(llmtest.Turn(
		llmtest.Done(models.AssistantMessage("hi"), nil),
	))

	sink := &errorSink{
		prefetchErr:     errors.New("prefetch failed"),
		syncTurnErr:     errors.New("sync failed"),
		onSessionEndErr: errors.New("session end failed"),
	}
	bus := events.New()
	var errorMessages []string
	bus.Subscribe(func(ctx context.Context, ev events.Event) error {
		if e, ok := ev.(events.ErrorEvent); ok {
			errorMessages = append(errorMessages, e.Message)
		}
		return nil
	})

	registry := tools.NewRegistry(".")
	perms := permissions.NewEngine(permissions.DefaultConfig())
	cfg := Config{
		SystemPrompt:      "You are helpful.",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionParallel,
		MemoryInjector:    sink,
		ShouldStop: func(ctx context.Context, turn TurnSummary) (bool, error) {
			return true, nil
		},
	}
	ag := New(cfg, client, registry, perms, bus)

	ctx := context.Background()
	if err := ag.Prompt(ctx, models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hello"})); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	for _, want := range []string{
		"memory prefetch: prefetch failed",
		"memory sync_turn: sync failed",
		"memory session_end: session end failed",
	} {
		if !slices.Contains(errorMessages, want) {
			t.Errorf("expected error message %q in %v", want, errorMessages)
		}
	}
}

func TestAgentNilInjectorDoesNotPanic(t *testing.T) {
	client := llmtest.Client(llmtest.Turn(
		llmtest.Done(models.AssistantMessage("hi"), nil),
	))

	bus := events.New()
	registry := tools.NewRegistry(".")
	perms := permissions.NewEngine(permissions.DefaultConfig())
	cfg := Config{
		SystemPrompt:      "You are helpful.",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionParallel,
		MemoryInjector:    nil,
		ShouldStop: func(ctx context.Context, turn TurnSummary) (bool, error) {
			return true, nil
		},
	}
	ag := New(cfg, client, registry, perms, bus)

	ctx := context.Background()
	if err := ag.Prompt(ctx, models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hello"})); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
}

func TestAgentWithModeNilInjectorDoesNotPanic(t *testing.T) {
	client := llmtest.Client(llmtest.Turn(
		llmtest.Done(models.AssistantMessage("hi"), nil),
	))

	bus := events.New()
	registry := tools.NewRegistry(".")
	perms := permissions.NewEngine(permissions.DefaultConfig())
	cfg := Config{
		SystemPrompt:      "You are helpful.",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionParallel,
		MemoryInjector:    nil,
		ContextManager:    contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192}),
	}
	ag := New(cfg, client, registry, perms, bus)

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("WithMode panicked: %v", r)
			}
		}()
		_ = ag.WithMode("explore")
	}()
}
