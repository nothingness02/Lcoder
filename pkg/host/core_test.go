package host

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/agentapi"
	"github.com/lcoder/lcoder/pkg/checkpoint"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/session"
	"github.com/lcoder/lcoder/pkg/task"
	"github.com/lcoder/lcoder/pkg/tools"
	"github.com/lcoder/lcoder/pkg/tools/builtin"
)

const testCWD = "host-test-cwd"

// newTestCore assembles a Core over a real agent served by the scripted LLM
// client, with real session/checkpoint stores under temp dirs.
func newTestCore(t *testing.T, client *llm.Client, perms *permissions.Engine, sess *session.Session, store *session.Store, cpStore checkpoint.Store, onSessionChange func(*session.Session)) (*Core, *events.Bus) {
	t.Helper()
	if perms == nil {
		perms = permissions.NewEngine(permissions.DefaultConfig())
	}
	reg := tools.NewRegistry(t.TempDir())
	for _, f := range builtin.Factories() {
		reg.RegisterBuiltin(f)
	}
	// A restore-capable context manager (checkpoint restore refuses one whose
	// internal services were never wired).
	mgr := contextmgr.NewManager(
		contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192, MaxOutput: 16384},
		contextmgr.WithSummarizer(func(_ context.Context, _ []models.AgentMessage, _ string) (string, error) {
			return "summary", nil
		}),
	)
	bus := events.New()
	ag := agent.New(agent.Config{
		SystemPrompt:    "x",
		Model:           models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ContextManager:  mgr,
		SessionID:       sess.ID,
		CheckpointStore: cpStore,
	}, client, reg, perms, bus)
	return NewCore(ag, bus, store, sess, testCWD, onSessionChange), bus
}

func newStore(t *testing.T) *session.Store {
	t.Helper()
	return session.NewStore(t.TempDir())
}

func mustCreate(t *testing.T, store *session.Store) *session.Session {
	t.Helper()
	sess, err := store.Create(testCWD)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return sess
}

func textMsg(role models.MessageRole, text string) models.AgentMessage {
	return models.NewAgentMessage(role, models.TextContent{Text: text})
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (c *Core) goalDriverDone() chan struct{} {
	c.goalMu.Lock()
	defer c.goalMu.Unlock()
	return c.goalDone
}

// The mirror is a synchronous bus subscriber registered by NewCore, so a probe
// subscribed AFTER NewCore observes the post-mirror state inside the same
// TurnEnd emission — which the run loop precedes with nothing and follows with
// the automatic checkpoint write. Asserting inside the probe that the session
// FILE already holds the turn's messages proves the "session on disk ≥
// checkpoint" invariant.
func TestMirrorPersistsTurnSynchronouslyBeforeCheckpoint(t *testing.T) {
	store := newStore(t)
	sess := mustCreate(t, store)
	cpStore := checkpoint.NewFileStore(t.TempDir())
	client := llmtest.Client(llmtest.Turn(llmtest.Done(textMsg(models.RoleAssistant, "turn reply"), nil)))
	core, bus := newTestCore(t, client, nil, sess, store, cpStore, nil)
	defer core.Close()

	var onDiskAtTurnEnd bool
	bus.Subscribe(func(_ context.Context, ev events.Event) error {
		if _, ok := ev.(events.TurnEndEvent); !ok {
			return nil
		}
		reloaded, err := store.LoadByID(testCWD, sess.ID)
		if err != nil {
			return nil // file may not exist yet only if the mirror failed
		}
		for _, m := range reloaded.ActiveMessages() {
			if m.Text() == "turn reply" {
				onDiskAtTurnEnd = true
			}
		}
		return nil
	})

	if err := core.Prompt(context.Background(), models.UserMessage("hi")); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if !onDiskAtTurnEnd {
		t.Fatal("at TurnEnd the session file did not yet contain the turn's assistant message")
	}
	// The automatic checkpoint is written at the turn boundary, after the
	// TurnEnd emission (and therefore after the mirror) returned.
	ids, err := cpStore.List()
	if err != nil || len(ids) == 0 {
		t.Fatalf("expected an automatic checkpoint after the turn, got ids=%v err=%v", ids, err)
	}
	// The user message is persisted at submit time (absorbed from the TUI
	// runner queue), the assistant message by the mirror.
	reloaded, err := store.LoadByID(testCWD, sess.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	var sawUser, sawAssistant bool
	for _, m := range reloaded.ActiveMessages() {
		if m.Role == models.RoleUser && m.Text() == "hi" {
			sawUser = true
		}
		if m.Text() == "turn reply" {
			sawAssistant = true
		}
	}
	if !sawUser || !sawAssistant {
		t.Fatalf("session file missing messages: user=%v assistant=%v", sawUser, sawAssistant)
	}
}

// OpenSession swaps messages, session id, task list, mirror target, and fires
// the session-change notification (the compaction-sink wiring).
func TestOpenSessionSwapsHistoryTasksAndMirror(t *testing.T) {
	store := newStore(t)
	sess0 := mustCreate(t, store)

	sess1 := mustCreate(t, store)
	if err := sess1.Append(models.UserMessage("older question")); err != nil {
		t.Fatal(err)
	}
	todoCall := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Type: "tool_call", ID: "t1", Name: task.ToolName,
		Arguments: map[string]any{"todos": []any{
			map[string]any{"text": "do x", "status": "pending"},
		}},
	})
	if err := sess1.Append(todoCall); err != nil {
		t.Fatal(err)
	}

	client := llmtest.Client(llmtest.Turn(llmtest.Done(textMsg(models.RoleAssistant, "new reply"), nil)))
	var notified []string
	core, _ := newTestCore(t, client, nil, sess0, store, nil, func(s *session.Session) {
		notified = append(notified, s.ID)
	})
	defer core.Close()
	if len(notified) != 1 || notified[0] != sess0.ID {
		t.Fatalf("NewCore must notify with the opening session, got %v", notified)
	}

	if err := core.OpenSession(sess1.ID); err != nil {
		t.Fatalf("open session: %v", err)
	}
	if core.SessionID() != sess1.ID {
		t.Fatalf("SessionID = %q, want %q", core.SessionID(), sess1.ID)
	}
	if got := core.AllMessages(); len(got) != 2 {
		t.Fatalf("AllMessages len = %d, want 2 (loaded history)", len(got))
	}
	tasks := core.Tasks()
	if len(tasks) != 1 || tasks[0].Text != "do x" {
		t.Fatalf("Tasks = %+v, want the restored todo_write entry", tasks)
	}
	if len(notified) != 2 || notified[1] != sess1.ID {
		t.Fatalf("session-change notification did not fire for the swap: %v", notified)
	}

	// The mirror follows: a new turn's output lands in sess1, not sess0.
	if err := core.Prompt(context.Background(), models.UserMessage("next")); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	reloaded, err := store.LoadByID(testCWD, sess1.ID)
	if err != nil {
		t.Fatalf("reload sess1: %v", err)
	}
	found := false
	for _, m := range reloaded.ActiveMessages() {
		if m.Text() == "new reply" {
			found = true
		}
	}
	if !found {
		t.Fatal("post-switch turn was not mirrored into the newly opened session")
	}
}

// NewSession clears the conversation and task list and notifies the wiring.
func TestNewSessionClearsState(t *testing.T) {
	store := newStore(t)
	sess0 := mustCreate(t, store)
	client := llmtest.Client(llmtest.Turn(llmtest.Done(textMsg(models.RoleAssistant, "reply"), nil)))
	var notified []string
	core, _ := newTestCore(t, client, nil, sess0, store, nil, func(s *session.Session) {
		notified = append(notified, s.ID)
	})
	defer core.Close()

	if err := core.Prompt(context.Background(), models.UserMessage("hi")); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if tm := core.currentRunner().TaskManager(); tm != nil {
		if err := tm.Restore(task.ManagerState{Tasks: []task.Task{{Text: "old", Status: task.StatusPending}}}); err != nil {
			t.Fatal(err)
		}
	}

	if err := core.NewSession(); err != nil {
		t.Fatalf("new session: %v", err)
	}
	if core.SessionID() == sess0.ID {
		t.Fatal("SessionID did not change after NewSession")
	}
	if got := core.AllMessages(); len(got) != 0 {
		t.Fatalf("AllMessages len = %d, want 0 after NewSession", len(got))
	}
	if got := core.Tasks(); len(got) != 0 {
		t.Fatalf("Tasks = %+v, want empty after NewSession", got)
	}
	if notified[len(notified)-1] != core.SessionID() {
		t.Fatalf("notification %v does not match new session %q", notified, core.SessionID())
	}
}

// TruncateAfter forks the session at the given message (/retry semantics):
// the agent context and the active branch are pruned, the abandoned tail
// stays on disk, and an unknown id is an error.
func TestTruncateAfterForksBranch(t *testing.T) {
	store := newStore(t)
	sess := mustCreate(t, store)
	for _, m := range []models.AgentMessage{
		models.UserMessage("u1"), textMsg(models.RoleAssistant, "a1"),
		models.UserMessage("u2"), textMsg(models.RoleAssistant, "a2"),
	} {
		if err := sess.Append(m); err != nil {
			t.Fatal(err)
		}
	}
	ids := make([]string, 0, 4)
	for _, m := range sess.ActiveMessages() {
		ids = append(ids, m.ID)
	}

	client := llmtest.Client(llmtest.Turn(llmtest.Done(textMsg(models.RoleAssistant, "x"), nil)))
	core, _ := newTestCore(t, client, nil, sess, store, nil, nil)
	defer core.Close()
	if err := core.OpenSession(sess.ID); err != nil {
		t.Fatalf("open session: %v", err)
	}

	if err := core.TruncateAfter("does-not-exist"); err == nil {
		t.Fatal("TruncateAfter with an unknown message id must fail")
	}
	// /retry forks at the message BEFORE the last user prompt (a1), so the
	// re-prompted u2 is not duplicated.
	if err := core.TruncateAfter(ids[1]); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if got := core.AllMessages(); len(got) != 2 || got[0].Text() != "u1" || got[1].Text() != "a1" {
		t.Fatalf("agent context after truncate = %v messages, want [u1 a1]", len(got))
	}
	// The fork is in-memory until the next append stamps branch_id, so assert
	// the branch view on the mirror's live session; the disk file keeps all
	// four messages (the abandoned tail stays reachable).
	live := core.mirror.activeSession()
	if got := live.ActiveMessages(); len(got) != 2 {
		t.Fatalf("active branch after fork = %d messages, want 2", len(got))
	}
	loaded, err := store.LoadByID(testCWD, sess.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := len(loaded.Messages); got != 4 {
		t.Fatalf("session holds %d messages, want 4 (abandoned tail stays reachable)", got)
	}
}

// countingConfirm records approval calls and always allows once.
type countingConfirm struct {
	mu    sync.Mutex
	calls int
}

func (f *countingConfirm) Confirm(context.Context, agentapi.ToolCallInfo) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return true, nil
}

func (f *countingConfirm) ConfirmWithScope(context.Context, agentapi.ToolCallInfo) (agentapi.ConfirmResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return agentapi.ConfirmResult{Allow: true, Scope: agentapi.ScopeOnce}, nil
}

func (f *countingConfirm) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func bashCallMsg(id, command string) models.AgentMessage {
	return models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Type: "tool_call", ID: id, Name: "bash",
		Arguments: map[string]any{"command": command},
	})
}

// SetMode swaps the runner (Mode reflects the switch) and the approval
// callback keeps working on the swapped-in agent.
func TestSetModeSwapsRunnerAndKeepsConfirm(t *testing.T) {
	store := newStore(t)
	sess := mustCreate(t, store)
	client := llmtest.Client(
		llmtest.Turn(llmtest.Done(bashCallMsg("b1", "echo host-confirm-1"), nil)),
		llmtest.Turn(llmtest.Done(textMsg(models.RoleAssistant, "done 1"), nil)),
		llmtest.Turn(llmtest.Done(bashCallMsg("b2", "echo host-confirm-2"), nil)),
		llmtest.Turn(llmtest.Done(textMsg(models.RoleAssistant, "done 2"), nil)),
	)
	perms := permissions.NewEngineFromRules([]permissions.Rule{
		{Tool: "bash", Pattern: "*", Decision: permissions.Ask},
	})
	core, _ := newTestCore(t, client, perms, sess, store, nil, nil)
	defer core.Close()

	confirm := &countingConfirm{}
	core.SetUserConfirm(confirm)
	if err := core.Prompt(context.Background(), models.UserMessage("run one")); err != nil {
		t.Fatalf("prompt 1: %v", err)
	}
	if confirm.callCount() != 1 {
		t.Fatalf("confirm calls = %d, want 1 (Ask rule on bash)", confirm.callCount())
	}

	before := core.currentRunner()
	if err := core.SetMode("plan"); err != nil {
		t.Fatalf("set mode: %v", err)
	}
	if core.Mode() != "plan" {
		t.Fatalf("Mode = %q, want plan", core.Mode())
	}
	if core.currentRunner() == before {
		t.Fatal("SetMode must swap the underlying runner instance")
	}

	if err := core.Prompt(context.Background(), models.UserMessage("run two")); err != nil {
		t.Fatalf("prompt 2: %v", err)
	}
	if confirm.callCount() != 2 {
		t.Fatalf("confirm calls = %d, want 2: approval callback lost across SetMode", confirm.callCount())
	}
}

// The goal driver pursues asynchronously: objective run, continuation run,
// model settles via update_goal, driver exits, and the session mirror kept up.
func TestGoalDriverRunsUntilComplete(t *testing.T) {
	store := newStore(t)
	sess := mustCreate(t, store)
	complete := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Type: "tool_call", ID: "c1", Name: "update_goal",
		Arguments: map[string]any{"status": "complete"},
	})
	client := llmtest.Client(
		llmtest.Turn(llmtest.Done(textMsg(models.RoleAssistant, "did some work"), nil)),
		llmtest.Turn(llmtest.Done(complete, nil)),
		llmtest.Turn(llmtest.Done(textMsg(models.RoleAssistant, "all done"), nil)),
	)
	core, _ := newTestCore(t, client, nil, sess, store, nil, nil)
	defer core.Close()

	core.StartGoal("fix the test", 0, 0)
	done := core.goalDriverDone()
	if done == nil {
		t.Fatal("StartGoal did not spawn the driver")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("goal driver did not exit after the goal completed")
	}

	g := core.Goal()
	if g == nil || g.Status != agentapi.GoalComplete {
		t.Fatalf("goal status = %v, want complete", g)
	}
	if g.TurnsUsed == 0 {
		t.Fatal("driver must count pursuit turns against the goal budget ledger")
	}
	var sawContinuation bool
	for _, m := range core.AllMessages() {
		if m.Role == models.RoleUser && strings.Contains(m.Text(), agent.GoalContinuationPromptText) {
			sawContinuation = true
		}
	}
	if !sawContinuation {
		t.Fatal("continuation prompt was never injected")
	}
	reloaded, err := store.LoadByID(testCWD, sess.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	found := false
	for _, m := range reloaded.ActiveMessages() {
		if m.Text() == "all done" {
			found = true
		}
	}
	if !found {
		t.Fatal("goal turns were not mirrored into the session")
	}
}

// Abort mid-pursuit interrupts the in-flight run; the driver pauses the goal
// and exits (the TUI's Esc behavior).
func TestGoalDriverPauseOnAbort(t *testing.T) {
	store := newStore(t)
	sess := mustCreate(t, store)
	client, adapter := llmtest.NewScript(llmtest.RepeatText("chunk ", 50))
	adapter.Delay = 10 * time.Millisecond
	core, _ := newTestCore(t, client, nil, sess, store, nil, nil)
	defer core.Close()

	core.StartGoal("long task", 0, 0)
	done := core.goalDriverDone()
	waitFor(t, "the first goal turn to start streaming", func() bool { return adapter.CallCount() >= 1 })

	core.Abort()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("goal driver did not exit after Abort")
	}
	g := core.Goal()
	if g == nil || g.Status != agentapi.GoalPaused {
		t.Fatalf("goal status = %v, want paused", g)
	}
	if g.BlockReason != string(events.EndReasonInterrupted) {
		t.Fatalf("BlockReason = %q, want %q", g.BlockReason, events.EndReasonInterrupted)
	}

	// A resume relaunches the driver and eventually pauses again on abort.
	core.ResumeGoal()
	if d := core.goalDriverDone(); d == nil {
		t.Fatal("ResumeGoal did not relaunch the driver")
	}
	waitFor(t, "the resumed goal turn to start", func() bool { return adapter.CallCount() >= 2 })
	core.Abort()
	waitFor(t, "the relaunched driver to exit", func() bool { return core.goalDriverDone() == nil })
	if g := core.Goal(); g == nil || g.Status != agentapi.GoalPaused {
		t.Fatalf("after resume+abort goal status = %v, want paused", g)
	}
}

// StartGoal while a driver is running replaces the goal record but must not
// spawn a second driver goroutine.
func TestStartGoalWhileRunningDoesNotReenter(t *testing.T) {
	store := newStore(t)
	sess := mustCreate(t, store)
	client, adapter := llmtest.NewScript(llmtest.RepeatText("chunk ", 50))
	adapter.Delay = 10 * time.Millisecond
	core, _ := newTestCore(t, client, nil, sess, store, nil, nil)
	defer core.Close()

	core.StartGoal("first", 0, 0)
	waitFor(t, "driver to start", func() bool { return adapter.CallCount() >= 1 })
	first := core.goalDriverDone()

	core.StartGoal("second", 0, 0)
	if core.goalDriverDone() != first {
		t.Fatal("re-entrant StartGoal spawned a second driver")
	}
	if g := core.Goal(); g == nil || g.Objective != "second" {
		t.Fatalf("goal record was not replaced: %+v", g)
	}
	// The new objective is steered into the in-flight pursuit, not dropped:
	// it is drained into the conversation at the next turn boundary.
	waitFor(t, "the steered objective to enter the conversation", func() bool {
		for _, m := range core.AllMessages() {
			if m.Role == models.RoleUser && m.Text() == "second" {
				return true
			}
		}
		return false
	})

	core.Abort()
	select {
	case <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("driver did not exit after Abort")
	}
}

// Save/List/Restore checkpoint round trip through the Core handle.
func TestCheckpointSaveListRestoreRoundTrip(t *testing.T) {
	store := newStore(t)
	sess := mustCreate(t, store)
	cpStore := checkpoint.NewFileStore(t.TempDir())
	client := llmtest.Client(
		llmtest.Turn(llmtest.Done(textMsg(models.RoleAssistant, "one"), nil)),
		llmtest.Turn(llmtest.Done(textMsg(models.RoleAssistant, "two"), nil)),
	)
	core, _ := newTestCore(t, client, nil, sess, store, cpStore, nil)
	defer core.Close()

	if err := core.Prompt(context.Background(), models.UserMessage("first")); err != nil {
		t.Fatalf("prompt 1: %v", err)
	}
	n1 := len(core.AllMessages())

	id, err := core.SaveCheckpoint()
	if err != nil {
		t.Fatalf("save checkpoint: %v", err)
	}
	if id != sess.ID {
		t.Fatalf("SaveCheckpoint id = %q, want the store key (session id %q)", id, sess.ID)
	}
	infos, err := core.ListCheckpoints()
	if err != nil {
		t.Fatalf("list checkpoints: %v", err)
	}
	found := false
	for _, info := range infos {
		if info.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListCheckpoints = %+v, want it to contain %q", infos, id)
	}

	// Diverge the live state WITHOUT a run: the checkpoint store is keyed by
	// session id, so a second turn's automatic checkpoint would legitimately
	// overwrite the manual one. Mutating the task list directly avoids that
	// and still proves restore really replaces state.
	if tm := core.currentRunner().TaskManager(); tm != nil {
		if err := tm.Restore(task.ManagerState{Tasks: []task.Task{{Text: "ghost", Status: task.StatusPending}}}); err != nil {
			t.Fatal(err)
		}
	}
	if got := core.Tasks(); len(got) != 1 {
		t.Fatalf("Tasks after direct mutation = %+v, want the ghost task", got)
	}

	if err := core.RestoreCheckpoint(id); err != nil {
		t.Fatalf("restore checkpoint: %v", err)
	}
	if got := len(core.AllMessages()); got != n1 {
		t.Fatalf("messages after restore = %d, want %d", got, n1)
	}
	if got := core.Tasks(); len(got) != 0 {
		t.Fatalf("Tasks after restore = %+v, want empty (checkpointed state)", got)
	}
}


// ---------------------------------------------------------------------------
// Run single-flight and busy guards (F1)
// ---------------------------------------------------------------------------

// While an ad-hoc run is in flight, every other run submission fails with
// ErrAgentBusy and the state-changing operations refuse to touch the core;
// once the run ends they work again.
func TestPromptSingleFlightAndBusyGuards(t *testing.T) {
	store := newStore(t)
	sess := mustCreate(t, store)
	client, adapter := llmtest.NewScript(llmtest.RepeatText("chunk ", 100))
	adapter.Delay = 5 * time.Millisecond
	core, _ := newTestCore(t, client, nil, sess, store, nil, nil)
	defer core.Close()

	runDone := make(chan error, 1)
	go func() { runDone <- core.Prompt(context.Background(), models.UserMessage("one")) }()
	waitFor(t, "the run to start streaming", func() bool { return adapter.CallCount() >= 1 })

	if !core.Running() {
		t.Fatal("Running() = false during an in-flight run")
	}
	if err := core.Prompt(context.Background(), models.UserMessage("two")); !errors.Is(err, ErrAgentBusy) {
		t.Fatalf("second Prompt = %v, want ErrAgentBusy", err)
	}
	if err := core.Continue(context.Background()); !errors.Is(err, ErrAgentBusy) {
		t.Fatalf("Continue = %v, want ErrAgentBusy", err)
	}
	if err := core.SetMode("plan"); !errors.Is(err, ErrAgentBusy) {
		t.Fatalf("SetMode = %v, want ErrAgentBusy", err)
	}
	if err := core.OpenSession(sess.ID); !errors.Is(err, ErrAgentBusy) {
		t.Fatalf("OpenSession = %v, want ErrAgentBusy", err)
	}
	if err := core.NewSession(); !errors.Is(err, ErrAgentBusy) {
		t.Fatalf("NewSession = %v, want ErrAgentBusy", err)
	}
	if err := core.TruncateAfter(""); !errors.Is(err, ErrAgentBusy) {
		t.Fatalf("TruncateAfter = %v, want ErrAgentBusy", err)
	}
	if err := core.RestoreCheckpoint("whatever"); !errors.Is(err, ErrAgentBusy) {
		t.Fatalf("RestoreCheckpoint = %v, want ErrAgentBusy", err)
	}

	core.Abort()
	<-runDone
	waitFor(t, "the run slot to be released", func() bool { return !core.Running() })

	if err := core.SetMode("plan"); err != nil {
		t.Fatalf("SetMode after the run finished: %v", err)
	}
	if core.Mode() != "plan" {
		t.Fatalf("Mode = %q, want plan", core.Mode())
	}
}

// A goal pursuit counts as busy for its whole lifetime — including the waits
// between turns — so ad-hoc runs and state changes are rejected while the
// driver is alive.
func TestGoalDriverCountsAsRunning(t *testing.T) {
	store := newStore(t)
	sess := mustCreate(t, store)
	client, adapter := llmtest.NewScript(llmtest.RepeatText("chunk ", 100))
	adapter.Delay = 5 * time.Millisecond
	core, _ := newTestCore(t, client, nil, sess, store, nil, nil)
	defer core.Close()

	core.StartGoal("long goal", 0, 0)
	waitFor(t, "the goal run to start", func() bool { return adapter.CallCount() >= 1 })

	if !core.Running() {
		t.Fatal("Running() = false during a pursuit")
	}
	if err := core.Prompt(context.Background(), models.UserMessage("hi")); !errors.Is(err, ErrAgentBusy) {
		t.Fatalf("Prompt during a pursuit = %v, want ErrAgentBusy", err)
	}
	if err := core.SetMode("plan"); !errors.Is(err, ErrAgentBusy) {
		t.Fatalf("SetMode during a pursuit = %v, want ErrAgentBusy", err)
	}

	core.Abort()
	waitFor(t, "the driver to exit", func() bool { return core.goalDriverDone() == nil })
	waitFor(t, "the run slot to be released", func() bool { return !core.Running() })
	if err := core.SetMode("plan"); err != nil {
		t.Fatalf("SetMode after the pursuit stopped: %v", err)
	}
}

// ---------------------------------------------------------------------------
// /retry in compacted-view coordinates (F2)
// ---------------------------------------------------------------------------

// With a compaction entry on the chain, the runner's view is
// [summary, kept..., post-entry...]. A retry fork point inside the kept
// region sits BEFORE the entry on the raw branch; the old implementation
// forked there and rebuilt the context from EffectiveMessages, resurrecting
// the full uncompressed history. The context cut must stay in the runner's
// compacted coordinates, and the session branch must keep the entry.
func TestTruncateAfterCompactionKeepsCompactedCoordinates(t *testing.T) {
	store := newStore(t)
	sess := mustCreate(t, store)
	history := []models.AgentMessage{
		models.UserMessage("old question"),
		textMsg(models.RoleAssistant, "old answer"),
		models.UserMessage("recent question"),
		textMsg(models.RoleAssistant, "recent answer"),
	}
	for _, m := range history {
		if err := sess.Append(m); err != nil {
			t.Fatal(err)
		}
	}
	// Fold [old question, old answer] away; the kept tail starts at "recent question".
	if err := sess.AppendCompactionEntry("SUMMARY", history[2].ID, 1000); err != nil {
		t.Fatal(err)
	}
	latest := models.UserMessage("latest question")
	if err := sess.Append(latest); err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(textMsg(models.RoleAssistant, "latest answer")); err != nil {
		t.Fatal(err)
	}

	client := llmtest.Client(llmtest.Turn(llmtest.Done(textMsg(models.RoleAssistant, "x"), nil)))
	core, _ := newTestCore(t, client, nil, sess, store, nil, nil)
	defer core.Close()
	if err := core.OpenSession(sess.ID); err != nil {
		t.Fatalf("open session: %v", err)
	}
	if got := core.AllMessages(); len(got) != 5 {
		t.Fatalf("compacted view = %d messages, want 5 [summary recent-q recent-a latest-q latest-a]", len(got))
	}

	// /retry of the latest prompt forks at the message before it ("recent
	// answer"), which precedes the compaction entry on the raw branch.
	if err := core.TruncateAfter(history[3].ID); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	got := core.AllMessages()
	if len(got) != 3 {
		t.Fatalf("context after truncate = %d messages, want 3 [summary recent-q recent-a]", len(got))
	}
	if got[0].Metadata["compacted"] != true {
		t.Error("head of the truncated context must be the compaction summary")
	}
	if got[1].Text() != "recent question" || got[2].Text() != "recent answer" {
		t.Fatalf("context after truncate = [%q %q %q], want [summary recent-q recent-a]",
			got[0].Text(), got[1].Text(), got[2].Text())
	}
	for _, m := range got {
		if strings.Contains(m.Text(), "old ") {
			t.Fatalf("folded-away history leaked back into the context: %q", m.Text())
		}
	}

	// The session branch keeps the entry, so a reload does not resurrect the
	// pre-compaction history either.
	live := core.mirror.activeSession()
	eff := live.EffectiveMessages()
	if len(eff) != 3 {
		t.Fatalf("session effective view after fork = %d messages, want 3", len(eff))
	}
	for _, m := range eff {
		if strings.Contains(m.Text(), "old ") {
			t.Fatalf("session effective view resurrected folded-away history: %q", m.Text())
		}
	}

	// An empty id forks at the root and clears the context.
	if err := core.TruncateAfter(""); err != nil {
		t.Fatalf("truncate at root: %v", err)
	}
	if got := core.AllMessages(); len(got) != 0 {
		t.Fatalf("context after root truncate = %d messages, want 0", len(got))
	}
}

// ---------------------------------------------------------------------------
// Goal driver state machine (F3)
// ---------------------------------------------------------------------------

// A ResumeGoal issued while the aborted driver is still unwinding must not be
// swallowed by the exit window: the driver settles its terminal state first,
// then the resume relaunches a fresh driver.
func TestResumeGoalImmediatelyAfterAbort(t *testing.T) {
	store := newStore(t)
	sess := mustCreate(t, store)
	client, adapter := llmtest.NewScript(llmtest.RepeatText("chunk ", 100))
	adapter.Delay = 5 * time.Millisecond
	core, _ := newTestCore(t, client, nil, sess, store, nil, nil)
	defer core.Close()

	core.StartGoal("long task", 0, 0)
	waitFor(t, "the first goal turn to start", func() bool { return adapter.CallCount() >= 1 })

	core.Abort()
	// Resume WITHOUT waiting for the driver to exit.
	core.ResumeGoal()
	waitFor(t, "the resumed driver to start a new run", func() bool { return adapter.CallCount() >= 2 })
	if d := core.goalDriverDone(); d == nil {
		t.Fatal("ResumeGoal did not relaunch the driver")
	}

	core.Abort()
	waitFor(t, "the relaunched driver to exit", func() bool { return core.goalDriverDone() == nil })
	if g := core.Goal(); g == nil || g.Status != agentapi.GoalPaused {
		t.Fatalf("after resume+abort goal status = %v, want paused", g)
	}
}

// Cancelling a goal mid-run stops the pursuit at the run boundary: the driver
// re-checks the goal before submitting and must not buy an extra turn.
func TestCancelGoalDuringRunBuysNoExtraTurn(t *testing.T) {
	store := newStore(t)
	sess := mustCreate(t, store)
	client, adapter := llmtest.NewScript(llmtest.RepeatText("chunk ", 100))
	adapter.Delay = 5 * time.Millisecond
	core, _ := newTestCore(t, client, nil, sess, store, nil, nil)
	defer core.Close()

	core.StartGoal("long task", 0, 0)
	waitFor(t, "the first goal turn to start", func() bool { return adapter.CallCount() >= 1 })

	core.CancelGoal()
	waitFor(t, "the driver to exit", func() bool { return core.goalDriverDone() == nil })
	if got := adapter.CallCount(); got != 1 {
		t.Fatalf("LLM calls = %d, want 1: a cancelled goal must not buy an extra pursuit turn", got)
	}
	if g := core.Goal(); g != nil {
		t.Fatalf("goal = %+v, want cleared", g)
	}
}

// Close cancels the driver and waits for it before detaching the mirror;
// afterwards run submissions and state changes fail with ErrCoreClosed and
// the goal methods no-op instead of leaking a goroutine.
func TestCloseStopsDriverAndRejectsCalls(t *testing.T) {
	store := newStore(t)
	sess := mustCreate(t, store)
	client, adapter := llmtest.NewScript(llmtest.RepeatText("chunk ", 100))
	adapter.Delay = 5 * time.Millisecond
	core, _ := newTestCore(t, client, nil, sess, store, nil, nil)

	core.StartGoal("long task", 0, 0)
	waitFor(t, "the goal run to start", func() bool { return adapter.CallCount() >= 1 })

	core.Close()
	if d := core.goalDriverDone(); d != nil {
		t.Fatal("Close returned while the driver was still running")
	}

	if err := core.Prompt(context.Background(), models.UserMessage("hi")); !errors.Is(err, ErrCoreClosed) {
		t.Fatalf("Prompt after Close = %v, want ErrCoreClosed", err)
	}
	if err := core.Continue(context.Background()); !errors.Is(err, ErrCoreClosed) {
		t.Fatalf("Continue after Close = %v, want ErrCoreClosed", err)
	}
	if err := core.SetMode("plan"); !errors.Is(err, ErrCoreClosed) {
		t.Fatalf("SetMode after Close = %v, want ErrCoreClosed", err)
	}
	if err := core.OpenSession(sess.ID); !errors.Is(err, ErrCoreClosed) {
		t.Fatalf("OpenSession after Close = %v, want ErrCoreClosed", err)
	}
	if err := core.NewSession(); !errors.Is(err, ErrCoreClosed) {
		t.Fatalf("NewSession after Close = %v, want ErrCoreClosed", err)
	}
	if err := core.TruncateAfter(""); !errors.Is(err, ErrCoreClosed) {
		t.Fatalf("TruncateAfter after Close = %v, want ErrCoreClosed", err)
	}
	if err := core.RestoreCheckpoint("x"); !errors.Is(err, ErrCoreClosed) {
		t.Fatalf("RestoreCheckpoint after Close = %v, want ErrCoreClosed", err)
	}

	core.StartGoal("again", 0, 0)
	core.ResumeGoal()
	if d := core.goalDriverDone(); d != nil {
		t.Fatal("StartGoal/ResumeGoal after Close spawned a driver")
	}
	core.Close() // idempotent
}
