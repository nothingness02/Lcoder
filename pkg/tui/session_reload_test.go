package tui

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/task"
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
	m := NewModel(bus, ag, sess, store, ".", "sess1", "openai/gpt-4o-mini", "dark", nil, nil, nil, nil, config.Config{}, nil, false)
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
		case blockUser:
			userText = b.raw
		case blockAssistant:
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
