package session

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func TestSessionLinearHistory(t *testing.T) {
	store := NewStore(t.TempDir())
	sess, err := store.Create("/tmp/proj")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	m1 := models.UserMessage("one")
	m2 := models.AssistantMessage("two")
	m3 := models.UserMessage("three")

	for _, m := range []models.AgentMessage{m1, m2, m3} {
		if err := sess.Append(m); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	active := sess.ActiveMessages()
	if len(active) != 3 {
		t.Fatalf("expected 3 active messages, got %d", len(active))
	}
	for i, want := range []string{"one", "two", "three"} {
		if active[i].Text() != want {
			t.Errorf("active[%d].Text = %q, want %q", i, active[i].Text(), want)
		}
	}

	// Parent chain should link each message to its predecessor.
	for i := 1; i < len(active); i++ {
		prev := active[i-1]
		cur := active[i]
		if cur.ParentID == nil || *cur.ParentID != prev.ID {
			t.Errorf("message %d parent = %v, want %s", i, cur.ParentID, prev.ID)
		}
	}
}

func TestSessionForkActiveMessages(t *testing.T) {
	store := NewStore(t.TempDir())
	sess, err := store.Create("/tmp/proj")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	m1 := models.UserMessage("root")
	m2 := models.AssistantMessage("main reply")
	if err := sess.Append(m1); err != nil {
		t.Fatalf("append m1: %v", err)
	}
	if err := sess.Append(m2); err != nil {
		t.Fatalf("append m2: %v", err)
	}

	branchID, err := sess.Fork(m1.ID)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if branchID == "" {
		t.Fatal("expected non-empty branch id")
	}

	m3 := models.AssistantMessage("branch reply")
	if err := sess.Append(m3); err != nil {
		t.Fatalf("append branch message: %v", err)
	}

	active := sess.ActiveMessages()
	if len(active) != 2 {
		t.Fatalf("expected 2 active messages on branch, got %d", len(active))
	}
	if active[0].Text() != "root" || active[1].Text() != "branch reply" {
		t.Errorf("active = %v, want [root branch reply]", texts(active))
	}

	// Main branch should still have both original messages.
	if err := sess.SwitchBranch(mainBranch); err != nil {
		t.Fatalf("switch to main: %v", err)
	}
	mainActive := sess.ActiveMessages()
	if len(mainActive) != 2 || mainActive[1].Text() != "main reply" {
		t.Errorf("main active = %v, want [root main reply]", texts(mainActive))
	}
}

func TestSessionForkUnknownMessage(t *testing.T) {
	store := NewStore(t.TempDir())
	sess, _ := store.Create("/tmp/proj")
	if _, err := sess.Fork("missing"); err == nil {
		t.Fatal("expected error forking unknown message")
	}
}

func TestSessionActiveMessagesCompatWithoutParentID(t *testing.T) {
	// Simulate loading an old linear session where messages have no parent ids.
	store := NewStore(t.TempDir())
	sess, _ := store.Create("/tmp/proj")
	for _, m := range []models.AgentMessage{
		models.UserMessage("a"),
		models.AssistantMessage("b"),
	} {
		if err := sess.Append(m); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	// Strip parent ids.
	for i := range sess.Messages {
		sess.Messages[i].ParentID = nil
	}

	active := sess.ActiveMessages()
	if len(active) != 2 {
		t.Fatalf("expected 2 active messages, got %d", len(active))
	}
}

func texts(msgs []models.AgentMessage) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Text()
	}
	return out
}
