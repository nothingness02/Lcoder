package builtin

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lcoder/lcoder/pkg/memory"
)

func setTestHome(t *testing.T, home string) {
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func TestMemoryToolAddAndList(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(filepath.Join(home, ".lcoder", "memory"), 0750); err != nil {
		t.Fatal(err)
	}
	setTestHome(t, home)

	store, err := memory.NewStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	tool := NewMemory(repo, store)

	res, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"action":  "add",
		"target":  "memory",
		"content": "Prefer parallel tool calls.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text() == "" {
		t.Fatal("expected non-empty result")
	}

	entries, err := store.GlobalEntries(memory.MemoryTarget)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0] != "Prefer parallel tool calls." {
		t.Fatalf("unexpected entries: %v", entries)
	}
}

func TestMemoryToolRejectsMissingOldText(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	setTestHome(t, home)
	store, err := memory.NewStore(tmp)
	if err != nil {
		t.Fatal(err)
	}
	tool := NewMemory(tmp, store)
	_, err = tool.Execute(context.Background(), "call-2", map[string]any{
		"action": "remove",
		"target": "memory",
	})
	if err == nil {
		t.Fatal("expected error for missing old_text")
	}
}

func TestMemoryToolReplace(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(filepath.Join(home, ".lcoder", "memory"), 0750); err != nil {
		t.Fatal(err)
	}
	setTestHome(t, home)

	store, err := memory.NewStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	tool := NewMemory(repo, store)

	if _, err := tool.Execute(context.Background(), "c1", map[string]any{
		"action":  "add",
		"target":  "memory",
		"content": "alpha",
	}); err != nil {
		t.Fatal(err)
	}
	res, err := tool.Execute(context.Background(), "c2", map[string]any{
		"action":   "replace",
		"target":   "memory",
		"old_text": "alph",
		"content":  "beta",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(res.Text(), "beta") && !contains(res.Text(), "Memory updated") {
		t.Fatalf("unexpected result text: %s", res.Text())
	}
	entries, _ := store.GlobalEntries(memory.MemoryTarget)
	if len(entries) != 1 || entries[0] != "beta" {
		t.Fatalf("unexpected entries: %v", entries)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || findSub(s, sub) >= 0)
}

func findSub(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
