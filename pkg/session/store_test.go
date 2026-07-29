package session

import (
	"os"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func TestCreateAppendLoad(t *testing.T) {
	dir, err := os.MkdirTemp("", "lcoder-session-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	store := NewStore(dir)
	sess, err := store.Create("/project")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	msg := models.UserMessage("hello")
	if err := sess.Append(msg); err != nil {
		t.Fatalf("append: %v", err)
	}

	loaded, err := store.Load(sess.Path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(loaded.Messages))
	}
	if loaded.Messages[0].Text() != "hello" {
		t.Fatalf("expected hello, got %s", loaded.Messages[0].Text())
	}
}

func TestCreateDoesNotWriteEmptySession(t *testing.T) {
	dir, err := os.MkdirTemp("", "lcoder-empty-session-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	store := NewStore(dir)
	sess, err := store.Create("/project")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := os.Stat(sess.Path); !os.IsNotExist(err) {
		t.Fatalf("expected no session file before first message, got %v", sess.Path)
	}

	list, err := store.List("/project")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty sessions list, got %d", len(list))
	}
}

// Subagent journals carry the subagent/meta marker so listings can skip them.
func TestIsSubagentJournal(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	sess, err := store.Create(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if sess.IsSubagentJournal() {
		t.Fatal("fresh session must not be a journal")
	}
	if err := sess.AppendCustomEntry("subagent/meta", []byte(`{"profile":"explore"}`)); err != nil {
		t.Fatal(err)
	}
	if !sess.IsSubagentJournal() {
		t.Fatal("session with subagent/meta must be a journal")
	}
}
