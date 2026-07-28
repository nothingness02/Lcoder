package agent

import (
	"context"
	"errors"
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
		contextmgr.WithSummarizer(func(_ context.Context, msgs []models.AgentMessage, _ string) (string, error) { return "s", nil }),
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

func TestAgentRecordsContextSnapshotOnCompaction(t *testing.T) {
	mgr := contextmgr.NewManager(
		contextmgr.TokenBudget{MaxTotal: 2000, TargetTotal: 100, ReserveOutput: 0},
		contextmgr.WithSummarizer(func(_ context.Context, msgs []models.AgentMessage, _ string) (string, error) { return "s", nil }),
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

	path := filepath.Join(dir, "context-turn-1-compaction.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected compaction snapshot at %s: %v", path, err)
	}
	if !strings.Contains(string(data), "Block: recent") {
		t.Fatal("snapshot missing recent block")
	}
}

func TestAgentDoesNotRecordContextSnapshotWhenDisabled(t *testing.T) {
	mgr := contextmgr.NewManager(
		contextmgr.TokenBudget{MaxTotal: 2000, TargetTotal: 100, ReserveOutput: 0},
		contextmgr.WithSummarizer(func(_ context.Context, msgs []models.AgentMessage, _ string) (string, error) { return "s", nil }),
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

// 压缩真正执行前先发 CompactionStarted 事件,顺序在 Committed 之前。
func TestAgentEmitsCompactionStartedBeforeCommit(t *testing.T) {
	mgr := contextmgr.NewManager(
		contextmgr.TokenBudget{MaxTotal: 2000, TargetTotal: 100, ReserveOutput: 0},
		contextmgr.WithSummarizer(func(_ context.Context, _ []models.AgentMessage, _ string) (string, error) { return "s", nil }),
		contextmgr.WithMinRecent(2),
	)
	var recent []models.AgentMessage
	for i := 0; i < 20; i++ {
		recent = append(recent, models.UserMessage(strings.Repeat("u", 200)))
		recent = append(recent, models.AssistantMessage(strings.Repeat("a", 200)))
	}
	mgr.SetBlock(contextmgr.NewBlock(contextmgr.BlockRecent, "recent", contextmgr.StabilityDynamic, 100, recent...))

	a := &Agent{mgr: mgr, bus: events.New()}
	var order []events.EventType
	unsub := a.bus.Subscribe(func(ctx context.Context, ev events.Event) error {
		order = append(order, ev.EventType())
		return nil
	})
	defer unsub()

	a.maybeCompact(context.Background(), 1)

	startIdx, commitIdx := -1, -1
	for i, typ := range order {
		switch typ {
		case events.CompactionStarted:
			startIdx = i
		case events.CompactionCommitted:
			commitIdx = i
		}
	}
	if startIdx == -1 {
		t.Fatal("expected CompactionStarted event before blocking compaction")
	}
	if commitIdx == -1 {
		t.Fatal("expected CompactionCommitted event")
	}
	if startIdx > commitIdx {
		t.Fatalf("started (idx %d) must precede committed (idx %d)", startIdx, commitIdx)
	}
}

// 无压缩压力时不发 CompactionStarted 事件(不打扰用户)。
func TestAgentNoCompactionStartedWithoutPressure(t *testing.T) {
	mgr := contextmgr.NewManager(
		contextmgr.TokenBudget{MaxTotal: 100000, TargetTotal: 90000, ReserveOutput: 1000},
		contextmgr.WithSummarizer(func(_ context.Context, _ []models.AgentMessage, _ string) (string, error) { return "s", nil }),
	)
	mgr.SetBlock(contextmgr.NewBlock(contextmgr.BlockRecent, "recent", contextmgr.StabilityDynamic, 100,
		models.UserMessage("hi"), models.AssistantMessage("hello")))

	a := &Agent{mgr: mgr, bus: events.New()}
	var saw bool
	unsub := a.bus.Subscribe(func(ctx context.Context, ev events.Event) error {
		if _, ok := ev.(events.CompactionStartedEvent); ok {
			saw = true
		}
		return nil
	})
	defer unsub()

	a.maybeCompact(context.Background(), 1)
	if saw {
		t.Fatal("CompactionStarted must not fire when no compaction will run")
	}
}

// 压缩提交时事件携带 summary、firstKeptID、tokensBefore。
func TestAgentCompactionEventCarriesPayload(t *testing.T) {
	mgr := contextmgr.NewManager(
		contextmgr.TokenBudget{MaxTotal: 2000, TargetTotal: 100, ReserveOutput: 0},
		contextmgr.WithSummarizer(func(_ context.Context, _ []models.AgentMessage, _ string) (string, error) { return "s", nil }),
		contextmgr.WithMinRecent(2),
	)
	var recent []models.AgentMessage
	for i := 0; i < 20; i++ {
		recent = append(recent, models.UserMessage(strings.Repeat("u", 200)))
		recent = append(recent, models.AssistantMessage(strings.Repeat("a", 200)))
	}
	mgr.SetBlock(contextmgr.NewBlock(contextmgr.BlockRecent, "recent", contextmgr.StabilityDynamic, 100, recent...))

	a := &Agent{mgr: mgr, bus: events.New()}
	var got events.CompactionCommittedEvent
	var saw bool
	unsub := a.bus.Subscribe(func(ctx context.Context, ev events.Event) error {
		if e, ok := ev.(events.CompactionCommittedEvent); ok {
			got, saw = e, true
		}
		return nil
	})
	defer unsub()

	a.maybeCompact(context.Background(), 1)
	if !saw {
		t.Fatal("expected CompactionCommittedEvent")
	}
	if got.Summary == "" || got.FirstKeptID == "" || got.TokensBefore <= 0 {
		t.Fatalf("event payload incomplete: %+v", got)
	}
	if got.Degraded {
		t.Fatal("healthy summarizer must not degrade")
	}
}

// A durable-record failure must not hide the fold from the UI: the context
// really is smaller, so the commit event still has to fire. Reporting only the
// error would leave the display showing a window that no longer exists.
func TestMaybeCompactEmitsCommitEvenWhenSinkFails(t *testing.T) {
	mgr := contextmgr.NewManager(
		contextmgr.TokenBudget{MaxTotal: 400, ReserveOutput: 0},
		contextmgr.WithMinRecent(1),
		contextmgr.WithSummarizer(contextmgr.SummarizeFunc(
			func(_ context.Context, _ []models.AgentMessage, _ string) (string, error) {
				return "SUMMARY", nil
			})),
		contextmgr.WithCompactionSink(func(contextmgr.FoldResult, []models.AgentMessage) error {
			return errors.New("disk full")
		}),
	)
	mgr.SetSystemPrompt("sys")
	var msgs []models.AgentMessage
	for i := 0; i < 20; i++ {
		msgs = append(msgs, models.UserMessage(strings.Repeat("x", 200)))
	}
	mgr.ReplaceRecent(msgs)

	bus := events.New()
	var committed, errored bool
	bus.Subscribe(func(_ context.Context, ev events.Event) error {
		switch ev.(type) {
		case events.CompactionCommittedEvent:
			committed = true
		case events.ErrorEvent:
			errored = true
		}
		return nil
	})

	ag := &Agent{cfg: Config{ContextManager: mgr}, mgr: mgr, bus: bus}
	ag.maybeCompact(context.Background(), 1)

	if !errored {
		t.Error("the sink failure must be surfaced as an error event")
	}
	if !committed {
		t.Error("the commit event must still fire: the fold stands despite the failed record")
	}
}
