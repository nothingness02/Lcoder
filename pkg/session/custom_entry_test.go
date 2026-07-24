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
	if err := sess.AppendCustomEntry("my-ext/state", json.RawMessage(`{ "count": 3 }`)); err != nil {
		t.Fatal(err)
	}

	// Data is normalized (compacted) at append time, so the in-memory bytes
	// match what a reload produces.
	if entries := sess.CustomEntries("my-ext/"); len(entries) != 1 || string(entries[0].Data) != `{"count":3}` {
		t.Fatalf("in-memory entries %+v", entries)
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
	if err := sess.Append(models.UserMessage("one")); err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendCustomEntry("ext/a", json.RawMessage(`{"branch":"main"}`)); err != nil {
		t.Fatal(err)
	}

	// Fork: new branch gets its own entries.
	if _, err := sess.Fork(""); err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(models.UserMessage("two")); err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendCustomEntry("ext/a", json.RawMessage(`{"branch":"fork"}`)); err != nil {
		t.Fatal(err)
	}

	entries := sess.CustomEntries("ext/")
	if len(entries) != 1 || string(entries[0].Data) != `{"branch":"fork"}` {
		t.Fatalf("fork branch entries %+v", entries)
	}
}

func TestAppendCustomEntryRejectsInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	sess, err := store.Create("/project")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendCustomEntry("ext/bad", json.RawMessage(`{"oops"`)); err == nil {
		t.Fatal("expected error for invalid JSON data")
	}

	// Regression: a rejected entry must not poison the in-memory session —
	// later appends still stage and persist.
	if err := sess.Append(models.UserMessage("still works")); err != nil {
		t.Fatalf("append after rejected entry: %v", err)
	}
	loaded, err := store.Load(sess.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.ActiveMessages(); len(got) != 1 || got[0].Role != models.RoleUser {
		t.Fatalf("active = %+v", got)
	}
	if entries := loaded.CustomEntries(""); len(entries) != 0 {
		t.Fatalf("entries %+v, want none", entries)
	}
}

func TestCustomEntryAsBranchHead(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	sess, err := store.Create("/project")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(models.UserMessage("one")); err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(models.UserMessage("two")); err != nil {
		t.Fatal(err)
	}
	// The custom entry is the last line, so after reload it is the branch head.
	if err := sess.AppendCustomEntry("ext/state", json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Load(sess.Path)
	if err != nil {
		t.Fatal(err)
	}
	active := loaded.ActiveMessages()
	if len(active) != 2 {
		t.Fatalf("active = %d, want 2: %+v", len(active), active)
	}
	for _, m := range active {
		if m.Role == models.RoleCustom || IsCustomEntry(m) {
			t.Fatal("custom entry leaked into ActiveMessages")
		}
	}
	if entries := loaded.CustomEntries("ext/"); len(entries) != 1 {
		t.Fatalf("entries %+v", entries)
	}
}

func TestCustomEntrySurvivesCompaction(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	sess, err := store.Create("/project")
	if err != nil {
		t.Fatal(err)
	}
	user := models.UserMessage("one")
	if err := sess.Append(user); err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendCustomEntry("ext/state", json.RawMessage(`{"pre":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendCompactionEntry("SUM", user.ID, 100); err != nil {
		t.Fatal(err)
	}

	// The compacted view never contains custom entries, but the raw entry
	// stays on the branch chain (design property: activeChain survives
	// compaction), so CustomEntries still sees the pre-compaction entry.
	assertViews := func(s *Session) {
		t.Helper()
		for _, m := range s.EffectiveMessages() {
			if m.Role == models.RoleCustom || IsCustomEntry(m) {
				t.Fatal("custom entry leaked into EffectiveMessages")
			}
		}
		entries := s.CustomEntries("ext/")
		if len(entries) != 1 || string(entries[0].Data) != `{"pre":true}` {
			t.Fatalf("entries %+v", entries)
		}
	}
	assertViews(sess)

	loaded, err := store.Load(sess.Path)
	if err != nil {
		t.Fatal(err)
	}
	assertViews(loaded)
}
