package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setTestHome(t *testing.T, home string) {
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func TestStoreReadsGlobalAndProjectEntries(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(filepath.Join(home, ".lcoder", "memory"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".lcoder", "memory"), 0750); err != nil {
		t.Fatal(err)
	}
	setTestHome(t, home)

	if err := os.WriteFile(filepath.Join(home, ".lcoder", "memory", "MEMORY.md"), []byte("global note"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".lcoder", "memory", "MEMORY.md"), []byte("project note"), 0640); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	text, err := store.MemoryText()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "global note") || !strings.Contains(text, "project note") {
		t.Fatalf("expected merged memory text, got:\n%s", text)
	}
}

func TestStoreAddAndDuplicateIgnored(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(filepath.Join(home, ".lcoder", "memory"), 0750); err != nil {
		t.Fatal(err)
	}
	setTestHome(t, home)

	store, err := NewStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Add(MemoryTarget, "first entry"); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(MemoryTarget, "first entry"); err != nil {
		t.Fatal(err)
	}
	entries, err := store.GlobalEntries(MemoryTarget)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d: %v", len(entries), entries)
	}
}

func TestStoreReplaceAndRemove(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(filepath.Join(home, ".lcoder", "memory"), 0750); err != nil {
		t.Fatal(err)
	}
	setTestHome(t, home)

	store, err := NewStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Add(MemoryTarget, "alpha"); err != nil {
		t.Fatal(err)
	}
	if err := store.Replace(MemoryTarget, "alpha", "beta"); err != nil {
		t.Fatal(err)
	}
	entries, err := store.GlobalEntries(MemoryTarget)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0] != "beta" {
		t.Fatalf("unexpected entries: %v", entries)
	}
	if err := store.Remove(MemoryTarget, "beta"); err != nil {
		t.Fatal(err)
	}
	entries, err = store.GlobalEntries(MemoryTarget)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}

func TestStoreAddRespectsLimit(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(filepath.Join(home, ".lcoder", "memory"), 0750); err != nil {
		t.Fatal(err)
	}
	setTestHome(t, home)

	store, err := NewStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	store.WithLimits(Limits{MemoryCharLimit: 10})
	if err := store.Add(MemoryTarget, "short"); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(MemoryTarget, "this is way too long"); err == nil {
		t.Fatal("expected limit error")
	}
}

func TestStoreAtomicWriteUsesTempFile(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(filepath.Join(home, ".lcoder", "memory"), 0750); err != nil {
		t.Fatal(err)
	}
	setTestHome(t, home)

	store, err := NewStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Add(MemoryTarget, "first entry"); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(home, ".lcoder", "memory", "*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary file should be removed after atomic write: %v", matches)
	}
	data, err := os.ReadFile(filepath.Join(home, ".lcoder", "memory", "MEMORY.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "first entry") {
		t.Fatalf("missing entry in file: %s", string(data))
	}
}

func TestStoreCacheInvalidatedOnAdd(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(filepath.Join(home, ".lcoder", "memory"), 0750); err != nil {
		t.Fatal(err)
	}
	setTestHome(t, home)

	store, err := NewStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Add(MemoryTarget, "alpha"); err != nil {
		t.Fatal(err)
	}
	entries, err := store.GlobalEntries(MemoryTarget)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if err := store.Add(MemoryTarget, "beta"); err != nil {
		t.Fatal(err)
	}
	entries, err = store.GlobalEntries(MemoryTarget)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	found := false
	for _, e := range entries {
		if e == "beta" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing beta in entries: %v", entries)
	}
}

func TestStoreUserChannelSeparate(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(filepath.Join(home, ".lcoder", "memory"), 0750); err != nil {
		t.Fatal(err)
	}
	setTestHome(t, home)

	store, err := NewStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Add(UserTarget, "user fact"); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(MemoryTarget, "agent fact"); err != nil {
		t.Fatal(err)
	}
	u, err := store.GlobalEntries(UserTarget)
	if err != nil {
		t.Fatal(err)
	}
	m, err := store.GlobalEntries(MemoryTarget)
	if err != nil {
		t.Fatal(err)
	}
	if len(u) != 1 || u[0] != "user fact" {
		t.Fatalf("unexpected user entries: %v", u)
	}
	if len(m) != 1 || m[0] != "agent fact" {
		t.Fatalf("unexpected memory entries: %v", m)
	}
}
