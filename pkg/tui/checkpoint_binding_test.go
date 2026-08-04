package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/host"
	"github.com/lcoder/lcoder/pkg/models"
)

func newCheckpointTestModel(ag *fakeAgent) *Model {
	if ag.SessionIDVal == "" {
		ag.SessionIDVal = "abc123"
	}
	m := NewModel(ag, host.Services{Bus: events.New()}, DisplayConfig{
		CWD:        ".",
		ModelRef:   "openai/gpt-4o-mini",
		ThemeStyle: "dark",
	})
	m.width = 80
	m.height = 24
	m.state = stateInput
	return m
}

func TestTUISaveCheckpoint(t *testing.T) {
	ag := &fakeAgent{ModeName: "code", Messages: []models.AgentMessage{models.UserMessage("hi")}}
	m := newCheckpointTestModel(ag)

	m.dispatchSlash("/save")

	if ag.SavedCheckpointCount != 1 {
		t.Fatalf("expected 1 saved checkpoint, got %d", ag.SavedCheckpointCount)
	}
	if !m.cmdPanel.visible || !strings.Contains(m.cmdPanel.text, "checkpoint saved: abc123") {
		t.Fatalf("expected success panel, got %+v", m.cmdPanel)
	}
}

func TestTUIRestoreCheckpoint(t *testing.T) {
	ag := &fakeAgent{ModeName: "code", RestoreMsgs: []models.AgentMessage{models.UserMessage("restored")}}
	m := newCheckpointTestModel(ag)

	m.dispatchSlash("/restore")

	if ag.RestoredCheckpoint != "abc123" {
		t.Fatalf("expected restore of session checkpoint abc123, got %q", ag.RestoredCheckpoint)
	}
	if len(m.blocks) != 1 || m.blocks[0].raw != "restored" {
		t.Fatalf("expected viewport rebuilt from restored messages, got %+v", m.blocks)
	}
	if !m.cmdPanel.visible || !strings.Contains(m.cmdPanel.text, "checkpoint restored") {
		t.Fatalf("expected success panel, got %+v", m.cmdPanel)
	}
}

func TestTUIListCheckpoints(t *testing.T) {
	ag := &fakeAgent{CheckpointIDs: []string{"abc123", "other"}}
	m := newCheckpointTestModel(ag)

	m.dispatchSlash("/checkpoints")

	if !m.cmdPanel.visible {
		t.Fatal("expected text panel visible")
	}
	if !strings.Contains(m.cmdPanel.text, "abc123") || !strings.Contains(m.cmdPanel.text, "other") {
		t.Fatalf("expected checkpoint ids in panel, got %q", m.cmdPanel.text)
	}
}

func TestTUISaveCheckpointError(t *testing.T) {
	ag := &fakeAgent{SaveCheckpointErr: errors.New("disk full")}
	m := newCheckpointTestModel(ag)

	m.dispatchSlash("/save")

	if !m.cmdPanel.visible || !strings.Contains(m.cmdPanel.text, "checkpoint failed: disk full") {
		t.Fatalf("expected error panel, got %+v", m.cmdPanel)
	}
}

func TestTUIRestoreCheckpointError(t *testing.T) {
	ag := &fakeAgent{RestoreErr: errors.New("not found")}
	m := newCheckpointTestModel(ag)

	m.dispatchSlash("/restore")

	if !m.cmdPanel.visible || !strings.Contains(m.cmdPanel.text, "restore failed: not found") {
		t.Fatalf("expected error panel, got %+v", m.cmdPanel)
	}
}

func TestTUIListCheckpointsEmpty(t *testing.T) {
	m := newCheckpointTestModel(&fakeAgent{})

	m.dispatchSlash("/checkpoints")

	if !m.cmdPanel.visible || !strings.Contains(m.cmdPanel.text, "no checkpoints saved") {
		t.Fatalf("expected empty panel, got %+v", m.cmdPanel)
	}
}

// Bubble Tea model assertions for the compiler.
var _ tea.Model = (*Model)(nil)
