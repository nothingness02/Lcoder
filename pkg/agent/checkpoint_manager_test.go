package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/lcoder/lcoder/pkg/checkpoint"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/tools"
)

func buildAgentWithStore(t *testing.T, store checkpoint.Store, sessionID string) *Agent {
	t.Helper()
	client := llmtest.Client(llmtest.Turn(llmtest.Done(models.AssistantMessage("ok"), nil)))
	registry := tools.NewRegistry(t.TempDir())
	bus := events.New()
	ag, err := NewBuilder().
		WithGatewayClient(client).
		WithRegistry(registry).
		WithEventBus(bus).
		WithPermissions(permissions.NewEngineFromRules(nil)).
		WithContextManager(testContextManager()).
		WithModel("openai", "gpt-4o-mini").
		WithSessionID(sessionID).
		WithCheckpointStore(store).
		Build()
	if err != nil {
		t.Fatalf("build agent: %v", err)
	}
	return ag
}

type errorStore struct {
	err error
}

func (s errorStore) Save(string, *checkpoint.Checkpoint) error { return s.err }
func (s errorStore) Load(string) (*checkpoint.Checkpoint, error) {
	return nil, errors.New("not implemented")
}
func (s errorStore) List() ([]string, error) { return nil, nil }
func (s errorStore) Delete(string) error     { return nil }

func TestCheckpointManagerManualCheckpointSaves(t *testing.T) {
	store := checkpoint.NewFileStore(t.TempDir())
	ag := buildAgentWithStore(t, store, "sess-manual")

	cp, err := ag.cpMgr.ManualCheckpoint()
	if err != nil {
		t.Fatalf("manual checkpoint: %v", err)
	}

	loaded, err := store.Load("sess-manual")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if loaded.Session.CheckpointID != cp.Session.CheckpointID {
		t.Fatalf("loaded checkpoint id mismatch")
	}
}

func TestCheckpointManagerMaybeCheckpointRespectsInterval(t *testing.T) {
	store := checkpoint.NewFileStore(t.TempDir())
	ag := buildAgentWithStore(t, store, "sess-interval")
	ag.cfg.CheckpointInterval = 3
	ag.cpMgr.interval = 3

	emit := func(context.Context, events.Event) {}

	for turn := 1; turn <= 5; turn++ {
		ag.cpMgr.MaybeCheckpoint(context.Background(), turn, checkpoint.ReasonAuto, emit)
	}

	versions, err := store.ListVersions("sess-interval")
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("expected 1 checkpoint at turn 3, got %d", len(versions))
	}

	loaded, err := store.Load("sess-interval")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if loaded.Runtime.Turn != 0 {
		// The turn counter in the checkpoint reflects the agent state at save
		// time; in this test no run has happened, so it should be 0.
		t.Errorf("checkpoint turn = %d, want 0", loaded.Runtime.Turn)
	}
}

func TestCheckpointManagerMaybeCheckpointEmitsErrorOnSaveFailure(t *testing.T) {
	wantErr := errors.New("disk full")
	ag := buildAgentWithStore(t, errorStore{err: wantErr}, "sess-error")

	var emitted []events.Event
	emit := func(_ context.Context, ev events.Event) {
		emitted = append(emitted, ev)
	}

	ag.cpMgr.MaybeCheckpoint(context.Background(), 1, checkpoint.ReasonAuto, emit)

	if len(emitted) != 1 {
		t.Fatalf("expected one error event, got %d", len(emitted))
	}
	errEv, ok := emitted[0].(events.ErrorEvent)
	if !ok {
		t.Fatalf("expected ErrorEvent, got %T", emitted[0])
	}
	if errEv.Message != "checkpoint save: "+wantErr.Error() {
		t.Errorf("error message = %q, want %q", errEv.Message, "checkpoint save: "+wantErr.Error())
	}
}

func TestCheckpointManagerRestoreDelegatesToAgent(t *testing.T) {
	store := checkpoint.NewFileStore(t.TempDir())
	ag := buildAgentWithStore(t, store, "sess-restore")

	cp, err := ag.cpMgr.ManualCheckpoint()
	if err != nil {
		t.Fatalf("manual checkpoint: %v", err)
	}

	// Restore into a fresh agent built without state.
	fresh := buildAgentWithStore(t, store, "sess-restore")
	if err := fresh.cpMgr.Restore(cp); err != nil {
		t.Fatalf("restore: %v", err)
	}

	if fresh.cfg.Model != ag.cfg.Model {
		t.Errorf("restored model = %+v, want %+v", fresh.cfg.Model, ag.cfg.Model)
	}
}
