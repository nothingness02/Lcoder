package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/observability"
)

// agent 在每轮前调用 mgr.MaybeCompact;提交时发 CompactionCommitted 事件。
func TestAgentEmitsCompactionCommitted(t *testing.T) {
	mgr := contextmgr.NewManager(
		contextmgr.TokenBudget{MaxTotal: 2000, TargetTotal: 100, ReserveOutput: 0},
		contextmgr.WithSummarizer(func(msgs []models.AgentMessage) (string, error) { return "s", nil }),
		contextmgr.WithMinRecent(2),
	)
	var recent []models.AgentMessage
	for i := 0; i < 20; i++ {
		recent = append(recent, models.UserMessage(strings.Repeat("u", 200)))
		recent = append(recent, models.AssistantMessage(strings.Repeat("a", 200)))
	}
	mgr.SetBlock(contextmgr.NewBlock(contextmgr.BlockRecent, "recent", contextmgr.StabilityDynamic, 100, recent...))

	a := &Agent{mgr: mgr, bus: events.New()}
	var got bool
	unsub := a.bus.Subscribe(func(ctx context.Context, ev events.Event) error {
		if _, ok := ev.(events.CompactionCommittedEvent); ok {
			got = true
		}
		return nil
	})
	defer unsub()

	a.maybeCompact(context.Background(), 1)
	if !got {
		t.Fatal("expected CompactionCommitted event to be emitted")
	}
}

func TestAgentRecordsContextSnapshotBeforeCompact(t *testing.T) {
	mgr := contextmgr.NewManager(
		contextmgr.TokenBudget{MaxTotal: 2000, TargetTotal: 100, ReserveOutput: 0},
		contextmgr.WithSummarizer(func(msgs []models.AgentMessage) (string, error) { return "s", nil }),
		contextmgr.WithMinRecent(2),
	)
	var recent []models.AgentMessage
	for i := 0; i < 20; i++ {
		recent = append(recent, models.UserMessage(strings.Repeat("u", 200)))
		recent = append(recent, models.AssistantMessage(strings.Repeat("a", 200)))
	}
	mgr.SetBlock(contextmgr.NewBlock(contextmgr.BlockRecent, "recent", contextmgr.StabilityDynamic, 100, recent...))

	dir := t.TempDir()
	recorder := observability.NewContextSnapshotRecorder("sess-snap", config.ContextSnapshotsConfig{
		Enabled:   true,
		OutputDir: dir,
	})

	a := &Agent{mgr: mgr, bus: events.New(), contextSnapshotRecorder: recorder}
	a.maybeCompact(context.Background(), 1)

	path := filepath.Join(dir, "context-turn-1-before-compact.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected before-compact snapshot at %s: %v", path, err)
	}
	if !strings.Contains(string(data), "Block: recent") {
		t.Fatal("snapshot missing recent block")
	}
}

func TestAgentDoesNotRecordContextSnapshotWhenDisabled(t *testing.T) {
	mgr := contextmgr.NewManager(
		contextmgr.TokenBudget{MaxTotal: 2000, TargetTotal: 100, ReserveOutput: 0},
		contextmgr.WithSummarizer(func(msgs []models.AgentMessage) (string, error) { return "s", nil }),
		contextmgr.WithMinRecent(2),
	)
	mgr.SetBlock(contextmgr.NewBlock(contextmgr.BlockRecent, "recent", contextmgr.StabilityDynamic, 100, models.UserMessage("hi")))

	dir := t.TempDir()
	recorder := observability.NewContextSnapshotRecorder("sess-snap", config.ContextSnapshotsConfig{
		Enabled:   false,
		OutputDir: dir,
	})

	a := &Agent{mgr: mgr, bus: events.New(), contextSnapshotRecorder: recorder}
	a.maybeCompact(context.Background(), 1)

	if _, err := os.ReadFile(filepath.Join(dir, "context-turn-1-before-compact.md")); err == nil {
		t.Fatal("expected no snapshot when disabled")
	}
}
