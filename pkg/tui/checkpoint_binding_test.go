package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/checkpoint"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
)

// fakeCheckpointStore is an in-memory checkpoint.Store for tests.
type fakeCheckpointStore struct {
	saved       []savedCheckpoint
	checkpoints map[string]*checkpoint.Checkpoint
	listErr     error
	loadErr     error
	saveErr     error
}

type savedCheckpoint struct {
	id string
	cp *checkpoint.Checkpoint
}

func (f *fakeCheckpointStore) Save(id string, cp *checkpoint.Checkpoint) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = append(f.saved, savedCheckpoint{id: id, cp: cp})
	return nil
}

func (f *fakeCheckpointStore) Load(id string) (*checkpoint.Checkpoint, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	if f.checkpoints == nil {
		return nil, checkpoint.ErrNotFound
	}
	cp, ok := f.checkpoints[id]
	if !ok {
		return nil, checkpoint.ErrNotFound
	}
	return cp, nil
}

func (f *fakeCheckpointStore) List() ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if f.checkpoints == nil {
		return nil, nil
	}
	var ids []string
	for id := range f.checkpoints {
		ids = append(ids, id)
	}
	return ids, nil
}

func (f *fakeCheckpointStore) Delete(id string) error {
	if f.checkpoints != nil {
		delete(f.checkpoints, id)
	}
	return nil
}

// fakeCheckpointAgent wraps fakeAgent and implements checkpoint.Source/Target.
type fakeCheckpointAgent struct {
	*fakeAgent
	checkpointErr error
	restoreErr    error
}

func (f *fakeCheckpointAgent) Checkpoint() (*checkpoint.Checkpoint, error) {
	if f.checkpointErr != nil {
		return nil, f.checkpointErr
	}
	return &checkpoint.Checkpoint{Agent: &checkpoint.AgentSnapshot{Mode: f.mode}}, nil
}

func (f *fakeCheckpointAgent) Restore(cp *checkpoint.Checkpoint) error {
	if f.restoreErr != nil {
		return f.restoreErr
	}
	f.mode = cp.Agent.Mode
	return nil
}

func newCheckpointTestModel(agent AgentRunner, chkStore checkpoint.Store) *Model {
	bus := events.New()
	sess := &fakeSession{id: "abc123"}
	m := NewModel(bus, agent, sess, &fakeSessionStore{}, ".", "abc123", "openai/gpt-4o-mini", "dark", nil, nil, nil, nil, config.Config{}, chkStore, false)
	m.width = 80
	m.height = 24
	m.state = stateInput
	return m
}

func TestTUISaveCheckpoint(t *testing.T) {
	ag := &fakeCheckpointAgent{fakeAgent: &fakeAgent{mode: "code", msgs: []models.AgentMessage{models.UserMessage("hi")}}}
	chkStore := &fakeCheckpointStore{}
	m := newCheckpointTestModel(ag, chkStore)

	m.dispatchSlash("/save")

	if len(chkStore.saved) != 1 {
		t.Fatalf("expected 1 saved checkpoint, got %d", len(chkStore.saved))
	}
	if chkStore.saved[0].id != "abc123" {
		t.Fatalf("expected session id abc123, got %s", chkStore.saved[0].id)
	}
	if chkStore.saved[0].cp.Agent.Mode != "code" {
		t.Fatalf("expected checkpoint mode code, got %s", chkStore.saved[0].cp.Agent.Mode)
	}
	if !m.cmdPanel.visible || !strings.Contains(m.cmdPanel.text, "checkpoint saved") {
		t.Fatalf("expected success panel, got %+v", m.cmdPanel)
	}
}

func TestTUIRestoreCheckpoint(t *testing.T) {
	ag := &fakeCheckpointAgent{fakeAgent: &fakeAgent{mode: "code"}}
	chkStore := &fakeCheckpointStore{
		checkpoints: map[string]*checkpoint.Checkpoint{
			"abc123": {Agent: &checkpoint.AgentSnapshot{Mode: "review"}},
		},
	}
	m := newCheckpointTestModel(ag, chkStore)

	m.dispatchSlash("/restore")

	if ag.Mode() != "review" {
		t.Fatalf("expected mode review after restore, got %s", ag.Mode())
	}
	if !m.cmdPanel.visible || !strings.Contains(m.cmdPanel.text, "checkpoint restored") {
		t.Fatalf("expected success panel, got %+v", m.cmdPanel)
	}
}

func TestTUIListCheckpoints(t *testing.T) {
	chkStore := &fakeCheckpointStore{
		checkpoints: map[string]*checkpoint.Checkpoint{
			"abc123": {Agent: &checkpoint.AgentSnapshot{}},
			"other":  {Agent: &checkpoint.AgentSnapshot{}},
		},
	}
	m := newCheckpointTestModel(&fakeCheckpointAgent{fakeAgent: &fakeAgent{}}, chkStore)

	m.dispatchSlash("/checkpoints")

	if !m.cmdPanel.visible {
		t.Fatal("expected text panel visible")
	}
	if !strings.Contains(m.cmdPanel.text, "abc123") || !strings.Contains(m.cmdPanel.text, "other") {
		t.Fatalf("expected checkpoint ids in panel, got %q", m.cmdPanel.text)
	}
}

func TestTUISaveCheckpointUnsupportedAgent(t *testing.T) {
	chkStore := &fakeCheckpointStore{}
	m := newCheckpointTestModel(&fakeAgent{mode: "code"}, chkStore)

	m.dispatchSlash("/save")

	if len(chkStore.saved) != 0 {
		t.Fatalf("expected no checkpoint saved, got %d", len(chkStore.saved))
	}
	if !m.cmdPanel.visible || !strings.Contains(m.cmdPanel.text, "agent does not support checkpoints") {
		t.Fatalf("expected unsupported error panel, got %+v", m.cmdPanel)
	}
}

func TestTUIRestoreCheckpointNoStore(t *testing.T) {
	ag := &fakeCheckpointAgent{fakeAgent: &fakeAgent{mode: "code"}}
	m := newCheckpointTestModel(ag, nil)

	m.dispatchSlash("/restore")

	if !m.cmdPanel.visible || !strings.Contains(m.cmdPanel.text, "no checkpoint store configured") {
		t.Fatalf("expected no store error panel, got %+v", m.cmdPanel)
	}
}

func TestTUIListCheckpointsEmpty(t *testing.T) {
	m := newCheckpointTestModel(&fakeCheckpointAgent{fakeAgent: &fakeAgent{}}, &fakeCheckpointStore{})

	m.dispatchSlash("/checkpoints")

	if !m.cmdPanel.visible || !strings.Contains(m.cmdPanel.text, "no checkpoints saved") {
		t.Fatalf("expected empty panel, got %+v", m.cmdPanel)
	}
}

// Ensure fakeCheckpointStore satisfies checkpoint.Store.
var _ checkpoint.Store = (*fakeCheckpointStore)(nil)

// Ensure fakeCheckpointAgent satisfies the TUI agent and checkpoint interfaces.
var (
	_ AgentRunner      = (*fakeCheckpointAgent)(nil)
	_ CheckpointSource = (*fakeCheckpointAgent)(nil)
	_ CheckpointTarget = (*fakeCheckpointAgent)(nil)
)

// Bubble Tea model assertions for the compiler.
var _ tea.Model = (*Model)(nil)
