package tui

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/task"
	"github.com/lcoder/lcoder/pkg/tui/components"
)

// newStartupModel builds a Model exactly the way tui.Run does at process start,
// with the core's context window already restored (cmd calls
// ag.SetMessages(sess.ActiveMessages()) before constructing the TUI).
func newStartupModel(prior []models.AgentMessage) *Model {
	ag := &fakeAgent{Messages: prior}
	// Simulate the host having rebuilt the task list from the loaded history.
	ag.TasksVal = tasksFromMessages(prior)
	return newTestCoreModel(ag)
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
// todo_write call rebuilds the task sidebar on startup, mirroring
// openSessionByID.
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

// TestOpenSessionSwitchesConversation verifies that picking a different session
// routes through CoreAPI.OpenSession and rebuilds the display from the core's
// swapped-in state.
func TestOpenSessionSwitchesConversation(t *testing.T) {
	prior1 := []models.AgentMessage{
		models.UserMessage("q1"),
		models.AssistantMessage("a1"),
	}
	prior2 := []models.AgentMessage{models.UserMessage("q2")}
	ag := &fakeAgent{
		Messages:     prior1,
		SessionIDVal: "s1",
		SessionMsgs:  map[string][]models.AgentMessage{"s2": prior2},
	}
	m := newTestCoreModel(ag)
	defer m.Close()
	m.width = 80
	m.height = 24

	m.openSessionByID("s2")

	if ag.SessionID() != "s2" {
		t.Fatalf("OpenSession did not update SessionID: got %q, want %q", ag.SessionID(), "s2")
	}
	got := ag.AllMessages()
	if len(got) != len(prior2) || got[0].Text() != "q2" {
		t.Fatalf("agent context window mismatch: got %+v", got)
	}
	var found bool
	for _, b := range m.blocks {
		if b.kind == components.BlockUser && b.raw == "q2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("display should rebuild from the opened session, got %+v", m.blocks)
	}
}

// TestOpenSessionSyncsTasks verifies that switching sessions also refreshes
// the UI task strip from the core's rebuilt task list.
func TestOpenSessionSyncsTasks(t *testing.T) {
	todo := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Name: task.ToolName,
		Arguments: map[string]any{"todos": []any{
			map[string]any{"text": "loaded task", "status": "pending"},
		}},
	})
	ag := &fakeAgent{
		SessionIDVal: "s1",
		SessionMsgs:  map[string][]models.AgentMessage{"s2": {models.UserMessage("plan"), todo}},
	}
	m := newTestCoreModel(ag)
	defer m.Close()
	m.width = 80
	m.height = 24

	m.openSessionByID("s2")

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
	ag := &fakeAgent{Messages: prior, TasksVal: tasksFromMessages(prior), SessionIDVal: "s1"}
	m := newTestCoreModel(ag)
	defer m.Close()
	m.width = 80
	m.height = 24
	m.state = stateInput

	oldID := ag.SessionID()
	m.history.add("old prompt")

	cmd := m.dispatchSlash("/new")
	if cmd != nil {
		t.Fatal("/new should not return a follow-up command")
	}

	if ag.NewSessionCount != 1 {
		t.Fatalf("/new should call NewSession once, got %d", ag.NewSessionCount)
	}
	if ag.SessionID() == oldID {
		t.Fatalf("/new should create a new session, still using %q", oldID)
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
	if len(m.history.items) != 0 {
		t.Fatalf("input history should be cleared, got %+v", m.history.items)
	}
}
