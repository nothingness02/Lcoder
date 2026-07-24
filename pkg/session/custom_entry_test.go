package session

import (
	"encoding/json"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func TestAppendCustomEntryRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	sess, err := store.Create("/project")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(models.UserMessage("hi")); err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendCustomEntry("my-ext/state", json.RawMessage(`{"count":3}`)); err != nil {
		t.Fatal(err)
	}

	// Reload: entry survives.
	loaded, err := store.Load(sess.Path)
	if err != nil {
		t.Fatal(err)
	}
	entries := loaded.CustomEntries("my-ext/")
	if len(entries) != 1 || entries[0].CustomType != "my-ext/state" {
		t.Fatalf("entries %+v", entries)
	}
	if string(entries[0].Data) != `{"count":3}` {
		t.Fatalf("data %s", entries[0].Data)
	}

	// Custom entries never enter the context views.
	for _, m := range loaded.ActiveMessages() {
		if m.Role == models.RoleCustom {
			t.Fatal("custom entry leaked into ActiveMessages")
		}
	}
	for _, m := range loaded.EffectiveMessages() {
		if m.Role == models.RoleCustom {
			t.Fatal("custom entry leaked into EffectiveMessages")
		}
	}
	if len(loaded.ActiveMessages()) != 1 {
		t.Fatalf("active = %d, want 1 (only the user message)", len(loaded.ActiveMessages()))
	}
}

func TestCustomEntriesBranchIsolation(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	sess, err := store.Create("/project")
	if err != nil {
		t.Fatal(err)
	}
	_ = sess.Append(models.UserMessage("one"))
	_ = sess.AppendCustomEntry("ext/a", json.RawMessage(`{"branch":"main"}`))

	// Fork: new branch gets its own entries.
	if _, err := sess.Fork(""); err != nil {
		t.Fatal(err)
	}
	_ = sess.Append(models.UserMessage("two"))
	_ = sess.AppendCustomEntry("ext/a", json.RawMessage(`{"branch":"fork"}`))

	entries := sess.CustomEntries("ext/")
	if len(entries) != 1 || string(entries[0].Data) != `{"branch":"fork"}` {
		t.Fatalf("fork branch entries %+v", entries)
	}
}
