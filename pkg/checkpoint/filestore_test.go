package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestFileStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	fs := NewFileStore(dir)

	id := "test-id"
	cp := &Checkpoint{
		Agent: &AgentSnapshot{Mode: "test-mode"},
	}

	if err := fs.Save(id, cp); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	sessionDir := filepath.Join(dir, "test-id")
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		t.Fatalf("session dir not created: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 checkpoint file, got %d", len(entries))
	}
	info, err := entries[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("file mode = %o, want %o", perm, 0o600)
		}
	}

	loaded, err := fs.Load(id)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.Agent.Mode != cp.Agent.Mode {
		t.Errorf("Mode = %q, want %q", loaded.Agent.Mode, cp.Agent.Mode)
	}
	if loaded.Version != CurrentVersion {
		t.Errorf("Version = %d, want %d", loaded.Version, CurrentVersion)
	}
	if loaded.CreatedAt.IsZero() {
		t.Error("CreatedAt not set")
	}

	ids, err := fs.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(ids) != 1 || ids[0] != id {
		t.Errorf("List = %v, want [%q]", ids, id)
	}

	if err := fs.Delete(id); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	_, err = fs.Load(id)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Load after delete error = %v, want ErrNotFound", err)
	}

	if err := fs.Delete(id); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete again error = %v, want ErrNotFound", err)
	}
}

func TestFileStoreRetentionKeepsLatestN(t *testing.T) {
	dir := t.TempDir()
	fs := NewFileStore(dir)
	fs.Retain = 3

	id := "sess-1"
	for i := 0; i < 5; i++ {
		cp := &Checkpoint{
			Agent:   &AgentSnapshot{Mode: "test-mode"},
			Runtime: &RuntimeSnapshot{Turn: i},
		}
		if err := fs.Save(id, cp); err != nil {
			t.Fatalf("Save failed: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	paths, err := fs.ListVersions(id)
	if err != nil {
		t.Fatalf("ListVersions failed: %v", err)
	}
	if len(paths) != 3 {
		t.Fatalf("expected 3 retained checkpoints, got %d", len(paths))
	}

	loaded, err := fs.LoadLatest(id)
	if err != nil {
		t.Fatalf("LoadLatest failed: %v", err)
	}
	if loaded.Runtime.Turn != 4 {
		t.Fatalf("latest turn = %d, want 4", loaded.Runtime.Turn)
	}
}

func TestFileStoreLoadLatestAcrossMultipleSessions(t *testing.T) {
	dir := t.TempDir()
	fs := NewFileStore(dir)

	for _, id := range []string{"sess-a", "sess-b"} {
		for turn := 0; turn < 2; turn++ {
			cp := &Checkpoint{
				Agent:   &AgentSnapshot{Mode: id},
				Runtime: &RuntimeSnapshot{Turn: turn},
			}
			if err := fs.Save(id, cp); err != nil {
				t.Fatalf("Save failed: %v", err)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	ids, err := fs.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("List = %v, want 2 sessions", ids)
	}

	loaded, err := fs.LoadLatest("sess-b")
	if err != nil {
		t.Fatalf("LoadLatest failed: %v", err)
	}
	if loaded.Agent.Mode != "sess-b" || loaded.Runtime.Turn != 1 {
		t.Fatalf("unexpected latest checkpoint: %+v", loaded)
	}
}
