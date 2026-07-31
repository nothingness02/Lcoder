package tui

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/session"
	"github.com/lcoder/lcoder/pkg/task"
	"github.com/lcoder/lcoder/pkg/tui/components"
)

// newStartupModel builds a Model exactly the way tui.Run does at process start,
// with the agent's context window already restored (main.go calls
// ag.SetMessages(sess.ActiveMessages()) before constructing the TUI).
func newStartupModel(prior []models.AgentMessage) *Model {
	bus := events.New()
	ag := &fakeAgent{Messages: prior}
	// Simulate the agent's TaskManager being restored from checkpoint/session.
	if tasks := tasksFromMessages(prior); len(tasks) > 0 {
		ag.TaskMgr = task.NewManager()
		_, _, _ = ag.TaskMgr.ReplaceAll(tasks)
	}
	sess := &fakeSession{ID: "sess1"}
	store := &fakeSessionStore{}
	m := NewModel(bus, ag, sess, store, ".", "sess1", "openai/gpt-4o-mini", "dark", nil, nil, nil, config.Config{}, nil, false, nil)
	return m
}

// TestNewModelRestoresPriorConversation reproduces the bug: reloading a session
// at startup must show the prior conversation, not a blank screen.
func TestNewModelRestoresPriorConversation(t *testing.T) {
	prior := []models.AgentMessage{
		models.UserMessage("first question"),
		models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "first answer"}),
	}
	m := newStartupModel(prior)
	defer m.Close()

	if len(m.blocks) == 0 {
		t.Fatal("startup-loaded model should rebuild blocks from the restored context window")
	}
	var userText, asstText string
	for _, b := range m.blocks {
		switch b.kind {
		case components.BlockUser:
			userText = b.raw
		case components.BlockAssistant:
			asstText = b.raw
		}
	}
	if userText != "first question" || asstText != "first answer" {
		t.Fatalf("blocks should mirror the conversation, got user=%q assistant=%q", userText, asstText)
	}
}

// TestNewModelRestoresAgentContextWindow verifies the user's second ask: after
// reload the agent's context window state is intact and matches what the TUI
// renders (they share the same source).
func TestNewModelRestoresAgentContextWindow(t *testing.T) {
	prior := []models.AgentMessage{
		models.UserMessage("q1"),
		models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "a1"}),
		models.UserMessage("q2"),
	}
	m := newStartupModel(prior)
	defer m.Close()

	got := m.agent.AllMessages()
	if len(got) != len(prior) {
		t.Fatalf("agent context window changed during reload: got %d msgs, want %d", len(got), len(prior))
	}
	for i := range prior {
		if got[i].Text() != prior[i].Text() {
			t.Fatalf("context window msg %d = %q, want %q", i, got[i].Text(), prior[i].Text())
		}
	}
}

// TestNewModelRestoresTaskSidebar checks that a session whose history ends in a
// todo_write call rebuilds the task sidebar on startup, mirroring loadSession.
func TestNewModelRestoresTaskSidebar(t *testing.T) {
	prior := []models.AgentMessage{
		models.UserMessage("plan it"),
		models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
			Name: task.ToolName,
			Arguments: map[string]any{"todos": []any{
				map[string]any{"text": "step one", "status": "done"},
				map[string]any{"text": "step two", "status": "pending"},
			}},
		}),
	}
	m := newStartupModel(prior)
	defer m.Close()

	if len(m.tasks) != 2 || m.tasks[0].Text != "step one" {
		t.Fatalf("startup should rebuild task sidebar from history, got %+v", m.tasks)
	}
}

// newTestModelWithStore builds a Model backed by a real session store, with the
// agent's context window and task manager initialized from the first session.
func newTestModelWithStore(t *testing.T, prior []models.AgentMessage) (*Model, *fakeAgent, *session.Store) {
	t.Helper()
	dir := t.TempDir()
	store := session.NewStore(dir)
	sess, err := store.Create("/project")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	for _, msg := range prior {
		if err := sess.Append(msg); err != nil {
			t.Fatalf("append message: %v", err)
		}
	}

	ag := &fakeAgent{Messages: sess.ActiveMessages(), TaskMgr: task.NewManager(), SessionIDVal: sess.ID}
	if ts := tasksFromMessages(prior); len(ts) > 0 {
		_, _, _ = ag.TaskMgr.ReplaceAll(ts)
	}

	bus := events.New()
	m := NewModel(bus, ag, sess, store, "/project", sess.ID, "openai/gpt-4o-mini", "dark", nil, nil, nil, config.Config{}, nil, false, nil)
	m.width = 80
	m.height = 24
	return m, ag, store
}

// TestLoadSessionSwitchesActiveWriter verifies that picking a different session
// makes the TUI write subsequent prompts to the new session, not the old one.
func TestLoadSessionSwitchesActiveWriter(t *testing.T) {
	prior1 := []models.AgentMessage{
		models.UserMessage("q1"),
		models.AssistantMessage("a1"),
	}
	m, ag, store := newTestModelWithStore(t, prior1)
	defer m.Close()

	sess2, err := store.Create("/project")
	if err != nil {
		t.Fatalf("create session 2: %v", err)
	}
	prior2 := []models.AgentMessage{models.UserMessage("q2")}
	for _, msg := range prior2 {
		if err := sess2.Append(msg); err != nil {
			t.Fatalf("append to session 2: %v", err)
		}
	}

	m.loadSession(sess2)

	if m.session.SessionID() != sess2.ID {
		t.Fatalf("loadSession did not switch m.session: got %q, want %q", m.session.SessionID(), sess2.ID)
	}
	if m.runner.session.SessionID() != sess2.ID {
		t.Fatalf("loadSession did not switch runner.session: got %q, want %q", m.runner.session.SessionID(), sess2.ID)
	}
	if ag.SessionID() != sess2.ID {
		t.Fatalf("loadSession did not update agent SessionID: got %q, want %q", ag.SessionID(), sess2.ID)
	}
	want := sess2.EffectiveMessages()
	got := ag.AllMessages()
	if len(got) != len(want) {
		t.Fatalf("agent context window mismatch: got %d msgs, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Text() != want[i].Text() {
			t.Fatalf("agent msg %d = %q, want %q", i, got[i].Text(), want[i].Text())
		}
	}
}

// TestLoadSessionSyncsTaskManager verifies that switching sessions also updates
// the agent's task manager so reminders reflect the loaded history.
func TestLoadSessionSyncsTaskManager(t *testing.T) {
	prior1 := []models.AgentMessage{models.UserMessage("plan")}
	m, ag, store := newTestModelWithStore(t, prior1)
	defer m.Close()

	sess2, err := store.Create("/project")
	if err != nil {
		t.Fatalf("create session 2: %v", err)
	}
	todo := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Name: task.ToolName,
		Arguments: map[string]any{"todos": []any{
			map[string]any{"text": "loaded task", "status": "pending"},
		}},
	})
	if err := sess2.Append(todo); err != nil {
		t.Fatalf("append todo: %v", err)
	}

	m.loadSession(sess2)

	tasks := ag.TaskMgr.List()
	if len(tasks) != 1 || tasks[0].Text != "loaded task" {
		t.Fatalf("task manager should sync with loaded session, got %+v", tasks)
	}
	if len(m.tasks) != 1 || m.tasks[0].Text != "loaded task" {
		t.Fatalf("UI task list should sync with loaded session, got %+v", m.tasks)
	}
}

// TestNewCommandCreatesSessionAndResetsState verifies that /new starts a fresh
// session and clears the agent context, UI, task list, and input history.
func TestNewCommandCreatesSessionAndResetsState(t *testing.T) {
	prior := []models.AgentMessage{
		models.UserMessage("plan"),
		models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
			Name: task.ToolName,
			Arguments: map[string]any{"todos": []any{
				map[string]any{"text": "old task", "status": "pending"},
			}},
		}),
	}
	m, ag, store := newTestModelWithStore(t, prior)
	defer m.Close()

	oldID := m.session.SessionID()
	m.history.add("old prompt")

	cmd := m.dispatchSlash("/new")
	if cmd != nil {
		t.Fatal("/new should not return a follow-up command")
	}

	if m.session.SessionID() == oldID {
		t.Fatalf("/new should create a new session, still using %q", oldID)
	}
	if m.runner.session.SessionID() != m.session.SessionID() {
		t.Fatalf("runner session %q does not match model session %q", m.runner.session.SessionID(), m.session.SessionID())
	}
	if ag.SessionID() != m.session.SessionID() {
		t.Fatalf("agent SessionID %q does not match model session %q", ag.SessionID(), m.session.SessionID())
	}
	if len(ag.AllMessages()) != 0 {
		t.Fatalf("agent context should be empty after /new, got %+v", ag.AllMessages())
	}
	if len(m.blocks) != 0 {
		t.Fatalf("conversation blocks should be cleared, got %d", len(m.blocks))
	}
	if len(m.tasks) != 0 {
		t.Fatalf("task list should be cleared, got %+v", m.tasks)
	}
	if len(ag.TaskMgr.List()) != 0 {
		t.Fatalf("agent task manager should be cleared, got %+v", ag.TaskMgr.List())
	}
	if len(m.history.items) != 0 {
		t.Fatalf("input history should be cleared, got %+v", m.history.items)
	}

	// The new session must belong to the same store and project.
	if _, err := store.LoadByID("/project", m.session.SessionID()); err == nil {
		// LoadByID succeeds only after a message is appended; /new should not
		// persist an empty session file. If it did, that would be unexpected.
		t.Fatalf("/new should not persist an empty session file")
	}
}

// TestCompactionSinkFollowsSessionSwitch verifies that a committed fold is
// recorded to the session currently active, not the one open at process start.
// Compactions are persisted by the context manager's CompactionSink rather than
// by an event subscriber, so what the sink must track is the model's session
// swap — which it learns about through SetOnSessionChange.
func TestCompactionSinkFollowsSessionSwitch(t *testing.T) {
	prior1 := []models.AgentMessage{models.UserMessage("q1")}
	m, _, store := newTestModelWithStore(t, prior1)
	defer m.Close()

	// Stand in for agentsetup.ActiveSession: the sink reads through this.
	var active *session.Session
	m.SetOnSessionChange(func(s *session.Session) { active = s })

	sess2, err := store.Create("/project")
	if err != nil {
		t.Fatalf("create session 2: %v", err)
	}
	if err := sess2.Append(models.UserMessage("q2")); err != nil {
		t.Fatalf("append to session 2: %v", err)
	}
	m.loadSession(sess2)

	if active == nil || active.ID != sess2.ID {
		t.Fatalf("session change was not reported: active=%v want %s", active, sess2.ID)
	}

	// What the sink does once the fold is committed.
	if err := active.AppendCompactionEntry("folded older messages", "kept-id", 1234); err != nil {
		t.Fatalf("AppendCompactionEntry: %v", err)
	}

	loaded, err := store.LoadByID("/project", sess2.ID)
	if err != nil {
		t.Fatalf("load session 2: %v", err)
	}
	var found bool
	for _, msg := range loaded.Messages {
		if session.IsCompactionEntry(msg) {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("compaction entry was not persisted to the switched session")
	}
}
