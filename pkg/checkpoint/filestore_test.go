package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
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

	wantPath := filepath.Join(dir, "test-id.checkpoint.json")
	info, err := os.Stat(wantPath)
	if err != nil {
		t.Fatalf("checkpoint file not created: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o644 {
			t.Errorf("file mode = %o, want %o", perm, 0o644)
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
