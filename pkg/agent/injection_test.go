package agent

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/task"
)

// --- todo injector ---------------------------------------------------------

func TestTodoInjectorQuietWindow(t *testing.T) {
	tm := task.NewManager()
	_, _, _ = tm.ReplaceAll([]task.Task{{Text: "step one", Status: task.StatusInProgress}})
	inj := newTodoInjector(tm)

	// The first observation of the task list counts as a write (turn 0), so
	// the reminder stays silent for the whole quiet window.
	for turn := 0; turn < todoReminderQuietTurns; turn++ {
		if got := inj.Inject(InjectContext{Turn: turn}); got != "" {
			t.Fatalf("turn %d: expected silence inside the quiet window, got %q", turn, got)
		}
	}
	// Both windows have expired: the reminder fires once...
	if got := inj.Inject(InjectContext{Turn: todoReminderQuietTurns}); got == "" {
		t.Fatalf("turn %d: expected reminder after the quiet window", todoReminderQuietTurns)
	}
	// ...then re-arms for another full window.
	for turn := todoReminderQuietTurns + 1; turn < 2*todoReminderQuietTurns; turn++ {
		if got := inj.Inject(InjectContext{Turn: turn}); got != "" {
			t.Fatalf("turn %d: expected silence after re-arming, got %q", turn, got)
		}
	}
	if got := inj.Inject(InjectContext{Turn: 2 * todoReminderQuietTurns}); got == "" {
		t.Fatalf("turn %d: expected second reminder", 2*todoReminderQuietTurns)
	}
}

func TestTodoInjectorDetectsWrites(t *testing.T) {
	tm := task.NewManager()
	_, _, _ = tm.ReplaceAll([]task.Task{{Text: "step one", Status: task.StatusInProgress}})
	inj := newTodoInjector(tm)

	if got := inj.Inject(InjectContext{Turn: 0}); got != "" {
		t.Fatalf("turn 0: expected silence right after the write, got %q", got)
	}
	if got := inj.Inject(InjectContext{Turn: todoReminderQuietTurns}); got == "" {
		t.Fatal("turn 10: expected reminder once the list went stale")
	}

	// A task-list change (observed at turn 11) restarts the write window even
	// though the reminder just fired.
	_, _, _ = tm.ReplaceAll([]task.Task{
		{Text: "step one", Status: task.StatusDone},
		{Text: "step two", Status: task.StatusInProgress},
	})
	for turn := 11; turn < 11+todoReminderQuietTurns; turn++ {
		if got := inj.Inject(InjectContext{Turn: turn}); got != "" {
			t.Fatalf("turn %d: expected silence after the write, got %q", turn, got)
		}
	}
	if got := inj.Inject(InjectContext{Turn: 11 + todoReminderQuietTurns}); got == "" {
		t.Fatal("expected reminder after the post-write window expired")
	}
}

func TestTodoInjectorSilentWhenNothingToDo(t *testing.T) {
	tm := task.NewManager()
	inj := newTodoInjector(tm)
	for _, turn := range []int{0, 10, 25} {
		if got := inj.Inject(InjectContext{Turn: turn}); got != "" {
			t.Fatalf("turn %d: expected silence with no tasks, got %q", turn, got)
		}
	}

	_, _, _ = tm.ReplaceAll([]task.Task{{Text: "done", Status: task.StatusDone}})
	for _, turn := range []int{26, 40} {
		if got := inj.Inject(InjectContext{Turn: turn}); got != "" {
			t.Fatalf("turn %d: expected silence when all tasks are done, got %q", turn, got)
		}
	}
}

func TestTodoInjectorReinjectsAfterCompaction(t *testing.T) {
	tm := task.NewManager()
	_, _, _ = tm.ReplaceAll([]task.Task{{Text: "step", Status: task.StatusInProgress}})
	inj := newTodoInjector(tm)

	if got := inj.Inject(InjectContext{Turn: 0}); got != "" {
		t.Fatalf("turn 0: expected silence right after the write, got %q", got)
	}
	inj.OnCompacted()
	if got := inj.Inject(InjectContext{Turn: 1}); got == "" {
		t.Fatal("turn 1: compaction must force one re-injection")
	}
	// The forced re-injection is one-shot; the write window rules again.
	if got := inj.Inject(InjectContext{Turn: 2}); got != "" {
		t.Fatalf("turn 2: expected the quiet cadence to resume, got %q", got)
	}
}

// --- mode injector ---------------------------------------------------------

func testModeInjector(mode ModeConfig) *modeInjector {
	mm := NewModeManager()
	mm.modes[mode.Name] = mode
	return newModeInjector(&Config{Mode: mode.Name, ModeManager: mm}, nil)
}

// The full cadence: full on entry, silent turns, sparse at a 2-turn distance,
// and a full refresh 5 turns after the last full prompt.
func TestModeInjectorCadence(t *testing.T) {
	inj := testModeInjector(ModeConfig{
		Name: "plan", SystemPrompt: "FULL TEXT", SparsePrompt: "SPARSE TEXT",
	})

	steps := []struct {
		turn    int
		want    string // substring expected in the injection; "" means silent
		notWant string
	}{
		{turn: 0, want: "FULL TEXT"},
		{turn: 1, want: ""},
		{turn: 2, want: "SPARSE TEXT", notWant: "FULL TEXT"},
		{turn: 3, want: ""},
		{turn: 4, want: "SPARSE TEXT", notWant: "FULL TEXT"},
		{turn: 5, want: "FULL TEXT"},
	}
	for _, s := range steps {
		got := inj.Inject(InjectContext{Turn: s.turn})
		if s.want == "" {
			if got != "" {
				t.Fatalf("turn %d: expected silence, got %q", s.turn, got)
			}
			continue
		}
		if !strings.Contains(got, s.want) {
			t.Fatalf("turn %d: expected %q in injection, got %q", s.turn, s.want, got)
		}
		if s.notWant != "" && strings.Contains(got, s.notWant) {
			t.Fatalf("turn %d: did not expect %q in injection, got %q", s.turn, s.notWant, got)
		}
	}
}

// Without a sparse prompt the sparse tier stays silent; only the entry full
// prompt and the 5-turn refresh are emitted. The full text no longer repeats
// every turn.
func TestModeInjectorNoSparseStaysSilent(t *testing.T) {
	inj := testModeInjector(ModeConfig{Name: "plan", SystemPrompt: "FULL TEXT"})

	if got := inj.Inject(InjectContext{Turn: 0}); !strings.Contains(got, "FULL TEXT") {
		t.Fatalf("turn 0: expected the full prompt, got %q", got)
	}
	for turn := 1; turn < modeReminderFullRefreshTurns; turn++ {
		if got := inj.Inject(InjectContext{Turn: turn}); got != "" {
			t.Fatalf("turn %d: expected silence without a sparse prompt, got %q", turn, got)
		}
	}
	if got := inj.Inject(InjectContext{Turn: modeReminderFullRefreshTurns}); !strings.Contains(got, "FULL TEXT") {
		t.Fatalf("turn %d: expected the full refresh, got %q", modeReminderFullRefreshTurns, got)
	}
}

func TestModeInjectorReinjectsAfterCompaction(t *testing.T) {
	inj := testModeInjector(ModeConfig{
		Name: "plan", SystemPrompt: "FULL TEXT", SparsePrompt: "SPARSE TEXT",
	})

	inj.Inject(InjectContext{Turn: 0}) // full
	inj.OnCompacted()
	if got := inj.Inject(InjectContext{Turn: 1}); !strings.Contains(got, "FULL TEXT") {
		t.Fatalf("turn 1: compaction must force a full re-injection, got %q", got)
	}
	if got := inj.Inject(InjectContext{Turn: 2}); got != "" {
		t.Fatalf("turn 2: expected the quiet cadence to resume, got %q", got)
	}
}

// A mode switch is a one-shot event: with an onSwitch callback wired, the
// release notice is handed off for persistence and stays out of the ephemeral
// injection, which then carries only the full prompt.
func TestModeInjectorSwitchNoticeGoesToCallback(t *testing.T) {
	inj := testModeInjector(ModeConfig{
		Name: "plan", SystemPrompt: "PLAN TEXT", SparsePrompt: "PLAN SPARSE",
	})
	var notices []string
	inj.onSwitch = func(s string) { notices = append(notices, s) }
	inj.cfg.ModeManager.modes["code"] = ModeConfig{Name: "code", SystemPrompt: "CODE TEXT"}

	inj.Inject(InjectContext{Turn: 0}) // plan full
	inj.cfg.Mode = "code"
	got := inj.Inject(InjectContext{Turn: 1})

	if len(notices) != 1 || !strings.Contains(notices[0], "switched from plan mode to code mode") {
		t.Fatalf("expected one release notice via callback, got %v", notices)
	}
	if strings.Contains(got, "switched from") {
		t.Fatalf("release notice must stay out of the ephemeral injection, got %q", got)
	}
	if !strings.Contains(got, "CODE TEXT") {
		t.Fatalf("switch must force the full prompt, got %q", got)
	}

	// One-shot: a switch-free turn fires no further notice.
	inj.Inject(InjectContext{Turn: 2})
	if len(notices) != 1 {
		t.Fatalf("release notice must be one-shot, got %v", notices)
	}
}

// Without an onSwitch callback the notice falls back to the ephemeral channel
// so the event is never dropped silently.
func TestModeInjectorSwitchNoticeFallbackEphemeral(t *testing.T) {
	inj := testModeInjector(ModeConfig{Name: "plan", SystemPrompt: "PLAN"})
	inj.cfg.ModeManager.modes["code"] = ModeConfig{Name: "code", SystemPrompt: "CODE"}

	inj.Inject(InjectContext{Turn: 0})
	inj.cfg.Mode = "code"
	got := inj.Inject(InjectContext{Turn: 1})
	if !strings.Contains(got, "switched from plan mode to code mode") {
		t.Fatalf("nil onSwitch must keep the notice ephemeral, got %q", got)
	}
}

// --- snapshot / restore ----------------------------------------------------

func TestInjectorSnapshotRestoreRoundTrip(t *testing.T) {
	tm := task.NewManager()
	_, _, _ = tm.ReplaceAll([]task.Task{{Text: "step", Status: task.StatusInProgress}})
	cfg := &Config{Mode: "plan", ModeManager: NewModeManager()}
	cfg.ModeManager.modes["plan"] = ModeConfig{Name: "plan", SystemPrompt: "FULL", SparsePrompt: "SPARSE"}

	mgr := newInjectionManager(tm, cfg, nil, nil)
	mgr.Collect(InjectContext{Turn: 0}) // todo: records the write; mode: full

	restored := newInjectionManager(tm, cfg, nil, nil)
	restored.Restore(mgr.Snapshot())

	// Without the restored bookkeeping, turn 1 would see a "new" task list
	// (write) and a "new" mode (full). Both must stay silent instead.
	if got := restored.Collect(InjectContext{Turn: 1}); len(got) != 0 {
		t.Fatalf("turn 1 after restore: expected silence, got %v", got)
	}
	// The restored cadence still fires on schedule.
	got := restored.Collect(InjectContext{Turn: 2})
	if len(got) != 1 || !strings.Contains(got[0], "SPARSE") {
		t.Fatalf("turn 2 after restore: expected only the sparse mode reminder, got %v", got)
	}
}

func TestInjectorRestoreIgnoresMissingVariants(t *testing.T) {
	// Old checkpoints carry no reminder state at all: restore must be a no-op,
	// so the next turn behaves like a fresh session (mode full prompt).
	cfg := &Config{Mode: "plan", ModeManager: NewModeManager()}
	cfg.ModeManager.modes["plan"] = ModeConfig{Name: "plan", SystemPrompt: "FULL"}
	mgr := newInjectionManager(task.NewManager(), cfg, nil, nil)
	mgr.Restore(nil)
	got := mgr.Collect(InjectContext{Turn: 0})
	if len(got) != 1 || !strings.Contains(got[0], "FULL") {
		t.Fatalf("expected the full mode prompt on a fresh cadence, got %v", got)
	}
}

// --- producer injectors (migrated from reminder_coordinator_test.go) -------

// quietManager builds an injection manager whose built-in injectors stay
// silent (no tasks, no mode manager) so only producer output is collected.
func quietManager(producers []ReminderProducer) *InjectionManager {
	return newInjectionManager(task.NewManager(), &Config{}, producers, nil)
}

func TestProducerInjectorsRunInOrder(t *testing.T) {
	mgr := quietManager([]ReminderProducer{
		func(msgs []models.AgentMessage) []string { return []string{"producer one"} },
		func(msgs []models.AgentMessage) []string { return []string{"producer two"} },
	})

	reminders := mgr.Collect(InjectContext{Turn: 0})
	if len(reminders) != 2 {
		t.Fatalf("expected 2 reminders, got %d: %v", len(reminders), reminders)
	}
	if reminders[0] != "producer one" || reminders[1] != "producer two" {
		t.Errorf("reminders = %v, want [producer one producer two]", reminders)
	}
}

func TestProducerInjectorPassesMessages(t *testing.T) {
	msgs := []models.AgentMessage{models.UserMessage("hello")}
	var got []models.AgentMessage
	mgr := quietManager([]ReminderProducer{
		func(m []models.AgentMessage) []string {
			got = m
			return nil
		},
	})

	mgr.Collect(InjectContext{Turn: 0, Messages: msgs})
	if len(got) != 1 || got[0].Text() != "hello" {
		t.Errorf("producer received %v, want user message", got)
	}
}

func TestInjectionManagerEmptyWhenAllSilent(t *testing.T) {
	mgr := quietManager(nil)
	if reminders := mgr.Collect(InjectContext{Turn: 0}); len(reminders) != 0 {
		t.Errorf("expected no reminders, got %v", reminders)
	}
}

// The todo reminder precedes producer output, matching the old coordinator's
// ordering contract.
func TestInjectionManagerTodoComesFirst(t *testing.T) {
	tm := task.NewManager()
	_, _, _ = tm.ReplaceAll([]task.Task{{Text: "step", Status: task.StatusInProgress}})
	mgr := newInjectionManager(tm, &Config{}, []ReminderProducer{
		func(msgs []models.AgentMessage) []string { return []string{"producer"} },
	}, nil)

	// Turn 0 records the task write (silent); jump past the quiet window.
	mgr.Collect(InjectContext{Turn: 0})
	reminders := mgr.Collect(InjectContext{Turn: todoReminderQuietTurns})
	if len(reminders) != 2 {
		t.Fatalf("expected todo + producer reminders, got %v", reminders)
	}
	if !strings.Contains(reminders[0], "todo") {
		t.Fatalf("expected the todo reminder first, got %q", reminders[0])
	}
	if reminders[1] != "producer" {
		t.Fatalf("expected the producer reminder second, got %q", reminders[1])
	}
}
