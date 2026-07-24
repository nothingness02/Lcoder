package tui

import (
	"context"
	"os"
	"testing"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/session"
)

// TestPersistSessionWritesAgentOutput reproduces the persistence defect: only
// the user message is appended at submit time, so the agent's assistant/tool
// output (which lives in the agent's context window, not the session) never
// reaches disk. persistSession must mirror the full window into the session.
func TestPersistSessionWritesAgentOutput(t *testing.T) {
	dir, err := os.MkdirTemp("", "lcoder-persist-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	store := session.NewStore(dir)
	sess, err := store.Create("/project")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Submit-time: only the user message is appended to the session.
	user := models.UserMessage("hi")
	if err := sess.Append(user); err != nil {
		t.Fatalf("append user: %v", err)
	}

	// The agent produced an assistant reply that lives only in its window.
	asst := models.AssistantMessage("hello there")
	ag := &fakeAgent{Messages: []models.AgentMessage{user, asst}}
	m := &Model{agent: ag, session: sess}

	m.persistSession()

	loaded, err := store.Load(sess.Path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var foundAsst bool
	for _, msg := range loaded.Messages {
		if msg.Role == models.RoleAssistant && msg.Text() == "hello there" {
			foundAsst = true
		}
	}
	if !foundAsst {
		t.Fatal("assistant message must persist to disk after a turn")
	}
}

// TestPersistFromEventMirrorsTurnEnd pins the ordering contract: mirroring
// happens on TurnEnd, which the agent loop emits synchronously before writing
// the automatic checkpoint — so the session on disk is never older than a
// checkpoint.
func TestPersistFromEventMirrorsTurnEnd(t *testing.T) {
	dir, err := os.MkdirTemp("", "lcoder-persist-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	store := session.NewStore(dir)
	sess, err := store.Create("/project")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	user := models.UserMessage("hi")
	if err := sess.Append(user); err != nil {
		t.Fatalf("append user: %v", err)
	}

	asst := models.AssistantMessage("hello there")
	ag := &fakeAgent{Messages: []models.AgentMessage{user, asst}}
	m := &Model{agent: ag, session: sess}

	if err := m.persistFromEvent(context.Background(), events.TurnEndEvent{
		Base:    events.Base{Type: events.TurnEnd, Turn: 1},
		Message: asst,
	}); err != nil {
		t.Fatalf("persistFromEvent: %v", err)
	}

	loaded, err := store.Load(sess.Path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, msg := range loaded.Messages {
		if msg.Role == models.RoleAssistant && msg.Text() == "hello there" {
			return
		}
	}
	t.Fatal("assistant message must reach disk at TurnEnd, before the checkpoint")
}
