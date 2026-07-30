package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func waitReady(t *testing.T, ix *FileIndex) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ix.Ready() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("FileIndex did not become ready within 2s")
}

func TestFileIndexScanAndMatch(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "main.go"), "x")
	mustWrite(t, filepath.Join(dir, "pkg", "loop.go"), "x")
	mustWrite(t, filepath.Join(dir, ".git", "config"), "x")
	mustWrite(t, filepath.Join(dir, "node_modules", "dep.js"), "x")
	mustWrite(t, filepath.Join(dir, ".hidden", "x.txt"), "x")

	ix := NewFileIndex(dir)
	defer ix.Stop()
	ix.EnsureStarted()
	waitReady(t, ix)

	got := ix.Matches("loop", 10)
	if !reflect.DeepEqual(got, []string{"pkg/loop.go"}) {
		t.Fatalf("Matches(loop) = %v", got)
	}

	all := ix.Matches("", 10)
	for _, f := range all {
		if f == ".git/config" || f == "node_modules/dep.js" || f == ".hidden/x.txt" {
			t.Fatalf("Matches included skipped dir entry: %q (all=%v)", f, all)
		}
	}
	if len(all) != 2 {
		t.Fatalf("Matches(\"\") = %v, want 2 entries", all)
	}
}

func TestScanFilesCap(t *testing.T) {
	dir := t.TempDir()
	for i := range 50 {
		mustWrite(t, filepath.Join(dir, fmt.Sprintf("f%02d.txt", i)), "x")
	}
	files, err := scanFiles(context.Background(), dir, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 10 {
		t.Fatalf("scanFiles returned %d files, want capped at 10", len(files))
	}
}

func TestScanFilesContextCancel(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.txt"), "x")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scanFiles(ctx, dir, 100); err == nil {
		t.Fatal("scanFiles with cancelled context should fail")
	}
}

func TestFileIndexFreshNoRescan(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.go"), "x")

	base := time.Now()
	now := base
	ix := NewFileIndex(dir)
	ix.now = func() time.Time { return now }
	defer ix.Stop()
	ix.EnsureStarted()
	waitReady(t, ix)

	// Within the TTL, a newly added file is NOT picked up: no rescan happens.
	mustWrite(t, filepath.Join(dir, "b.go"), "x")
	now = base.Add(5 * time.Second)
	ix.EnsureStarted()
	if got := ix.Matches("b.go", 10); len(got) != 0 {
		t.Fatalf("expected no rescan within TTL, got %v", got)
	}
}

func TestFileIndexTTLRescan(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.go"), "x")

	base := time.Now()
	now := base
	ix := NewFileIndex(dir)
	ix.now = func() time.Time { return now }
	defer ix.Stop()
	ix.EnsureStarted()
	waitReady(t, ix)

	// Past the TTL, EnsureStarted rescans in the background; the old list keeps
	// serving until the new one lands.
	mustWrite(t, filepath.Join(dir, "b.go"), "x")
	now = base.Add(time.Minute)
	ix.EnsureStarted()
	if got := ix.Matches("a.go", 10); len(got) != 1 {
		t.Fatalf("old list should keep serving during rescan, got %v", got)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := ix.Matches("b.go", 10); len(got) == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("rescan did not pick up new file within 2s")
}

func TestFileIndexStop(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.go"), "x")
	ix := NewFileIndex(dir)
	ix.Stop()
	ix.EnsureStarted()
	// Give the goroutine a chance to (not) run.
	time.Sleep(50 * time.Millisecond)
	if ix.Ready() {
		t.Fatal("stopped index must not become ready")
	}
}

func TestFuzzyMatchPathsLimit(t *testing.T) {
	files := []string{"a1.go", "a2.go", "a3.go", "a4.go"}
	if got := fuzzyMatchPaths(files, "", 2); len(got) != 2 {
		t.Fatalf("empty partial should cap at limit, got %v", got)
	}
	if got := fuzzyMatchPaths(files, "a", 3); len(got) != 3 {
		t.Fatalf("fuzzy matches should cap at limit, got %v", got)
	}
}

// stubSuggester is a synchronous fileSuggester for wiring tests.
type stubSuggester struct {
	ready   bool
	items   []string
	started int
}

func (s *stubSuggester) EnsureStarted()                          { s.started++ }
func (s *stubSuggester) Stop()                                   {}
func (s *stubSuggester) Ready() bool                             { return s.ready }
func (s *stubSuggester) Matches(string, int) []string            { return s.items }

func TestRefreshMenuIndexingPlaceholder(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	stub := &stubSuggester{ready: false}
	m.fileSuggester = stub

	m.input.textarea.SetValue("@ma")
	m.refreshMenu()

	if !m.fileMenuVisible || !m.fileMenuIndexing {
		t.Fatalf("expected indexing placeholder (visible=%v indexing=%v)", m.fileMenuVisible, m.fileMenuIndexing)
	}
	if stub.started == 0 {
		t.Fatal("refreshMenu should poke EnsureStarted for TTL refresh")
	}
}

func TestRefreshMenuServesStubItems(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	m.fileSuggester = &stubSuggester{ready: true, items: []string{"main.go"}}

	m.input.textarea.SetValue("@ma")
	m.refreshMenu()

	if m.fileMenuIndexing {
		t.Fatal("ready suggester must not show the indexing placeholder")
	}
	if !m.fileMenuVisible || len(m.fileMenuItems) != 1 || m.fileMenuItems[0] != "main.go" {
		t.Fatalf("expected stub items served, got visible=%v items=%v", m.fileMenuVisible, m.fileMenuItems)
	}
}
