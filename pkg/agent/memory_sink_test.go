package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/memory"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/tools"
	"github.com/stretchr/testify/require"
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
	err := ag.Prompt(ctx, models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hello"}))
	require.NoError(t, err)

	require.Contains(t, sink.prefetchQueries, "hello")
	require.Len(t, sink.synced, 1)
	require.Equal(t, "hello", sink.synced[0][0])
	require.Equal(t, "hello back", sink.synced[0][1])
	require.Len(t, sink.ended, 1)
	require.Equal(t, "test-session", sink.ended[0].SessionID)
	require.Equal(t, 1, sink.ended[0].TurnCount, "expected one completed turn")
}

type plainInjector struct{ called bool }

func (p *plainInjector) Prefetch(ctx context.Context, query string) error {
	p.called = true
	return nil
}

func TestAgentSkipsSinkHooksForPlainInjector(t *testing.T) {
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
	err := ag.Prompt(ctx, models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hello"}))
	require.NoError(t, err)

	require.True(t, inj.called, "plain injector Prefetch should be called")
}

type errorSink struct {
	prefetchErr    error
	syncTurnErr    error
	onSessionEndErr error
}

func (e *errorSink) Prefetch(ctx context.Context, query string) error { return e.prefetchErr }
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
	err := ag.Prompt(ctx, models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hello"}))
	require.NoError(t, err)

	require.Contains(t, errorMessages, "memory prefetch: prefetch failed")
	require.Contains(t, errorMessages, "memory sync_turn: sync failed")
	require.Contains(t, errorMessages, "memory session_end: session end failed")
}

func TestAgentTypedNilInjectorDoesNotPanic(t *testing.T) {
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
		MemoryInjector:    (*memory.Injector)(nil),
		ShouldStop: func(ctx context.Context, turn TurnSummary) (bool, error) {
			return true, nil
		},
	}
	ag := New(cfg, client, registry, perms, bus)

	ctx := context.Background()
	err := ag.Prompt(ctx, models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hello"}))
	require.NoError(t, err)
}
