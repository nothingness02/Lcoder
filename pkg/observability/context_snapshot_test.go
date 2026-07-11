package observability

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/models"
)

func TestContextSnapshotRecorderDisabledWritesNothing(t *testing.T) {
	dir := t.TempDir()
	recorder := NewContextSnapshotRecorder("sess-1", config.ContextSnapshotsConfig{
		Enabled:   false,
		OutputDir: dir,
	})
	state := &contextmgr.ManagerState{Blocks: []contextmgr.BlockState{}}
	if err := recorder.Record(state, "before-compact", 0); err != nil {
		t.Fatalf("record when disabled: %v", err)
	}
	if _, err := os.ReadFile(filepath.Join(dir, "context-turn-0-before-compact.md")); err == nil {
		t.Fatal("expected no file when disabled")
	}
}

func TestContextSnapshotRecorderRenderAndSave(t *testing.T) {
	dir := t.TempDir()
	recorder := NewContextSnapshotRecorder("sess-2", config.ContextSnapshotsConfig{
		Enabled:   true,
		OutputDir: dir,
	})

	state := &contextmgr.ManagerState{
		Budget: contextmgr.TokenBudget{MaxTotal: 1000, TargetTotal: 900},
		Blocks: []contextmgr.BlockState{
			{
				Kind:     contextmgr.BlockSystem,
				Name:     "system",
				Priority: 100,
				Stability: contextmgr.StabilityStatic,
				Messages: []models.AgentMessage{
					models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: "You are a helpful assistant."}),
				},
			},
			{
				Kind:     contextmgr.BlockRecent,
				Name:     "recent",
				Priority: 100,
				Stability: contextmgr.StabilityDynamic,
				Messages: []models.AgentMessage{
					models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hello"}),
					models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "hi there"}),
				},
			},
		},
	}

	if err := recorder.Record(state, "before-compact", 1); err != nil {
		t.Fatalf("record: %v", err)
	}

	path := filepath.Join(dir, "context-turn-1-before-compact.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	rendered := string(data)
	if !strings.Contains(rendered, "Context Snapshot") {
		t.Fatal("missing snapshot title")
	}
	if !strings.Contains(rendered, "Block: system") {
		t.Fatal("missing system block")
	}
	if !strings.Contains(rendered, "Block: recent") {
		t.Fatal("missing recent block")
	}
	if !strings.Contains(rendered, "hello") {
		t.Fatal("missing message content")
	}
}

func TestContextSnapshotRecorderMaxMessages(t *testing.T) {
	dir := t.TempDir()
	recorder := NewContextSnapshotRecorder("sess-3", config.ContextSnapshotsConfig{
		Enabled:             true,
		OutputDir:           dir,
		MaxMessagesPerBlock: 1,
	})

	state := &contextmgr.ManagerState{
		Blocks: []contextmgr.BlockState{
			{
				Kind:     contextmgr.BlockRecent,
				Name:     "recent",
				Messages: []models.AgentMessage{
					models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "first"}),
					models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "second"}),
				},
			},
		},
	}

	if err := recorder.Record(state, "end", 0); err != nil {
		t.Fatalf("record: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "context-turn-0-end.md"))
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if strings.Count(string(data), "first") != 1 {
		t.Fatalf("expected only first message to be rendered")
	}
	if strings.Contains(string(data), "second") {
		t.Fatalf("second message should be truncated")
	}
}

func TestContextSnapshotRecorderSetEnabled(t *testing.T) {
	dir := t.TempDir()
	recorder := NewContextSnapshotRecorder("sess-4", config.ContextSnapshotsConfig{
		Enabled:   false,
		OutputDir: dir,
	})

	state := &contextmgr.ManagerState{Blocks: []contextmgr.BlockState{}}

	// Disabled: no file written.
	_ = recorder.Record(state, "end", 0)
	if _, err := os.ReadFile(filepath.Join(dir, "context-turn-0-end.md")); err == nil {
		t.Fatal("expected no file when disabled")
	}

	// Enabled: file written.
	recorder.SetEnabled(true)
	_ = recorder.Record(state, "end", 0)
	if _, err := os.ReadFile(filepath.Join(dir, "context-turn-0-end.md")); err != nil {
		t.Fatalf("expected file after enabling: %v", err)
	}
}
