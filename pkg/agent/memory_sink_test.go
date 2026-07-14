package agent

import (
	"context"
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
}
