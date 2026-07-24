package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func writeManifest(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extension.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverFindsManifests(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, filepath.Join(root, "alpha"), "name: alpha\nversion: 0.1.0\ncommand: [\"go\", \"run\", \".\"]\n")
	writeManifest(t, filepath.Join(root, "beta"), "name: beta\ncommand: [\"python\", \"b.py\"]\nenv:\n  KEY: v\n")
	// A directory without a manifest is ignored.
	_ = os.MkdirAll(filepath.Join(root, "not-an-ext"), 0o755)

	found, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Fatalf("found %d, want 2", len(found))
	}
	if found[0].Name != "alpha" || found[1].Name != "beta" {
		t.Fatalf("order/names: %+v", found)
	}
	if found[1].Env["KEY"] != "v" {
		t.Fatalf("env: %+v", found[1].Env)
	}
	// Dir is recorded so the process can spawn with the extension dir as cwd.
	if found[0].Dir != filepath.Join(root, "alpha") {
		t.Fatalf("dir %q", found[0].Dir)
	}
}

func TestDiscoverMissingRootIsEmpty(t *testing.T) {
	found, err := Discover(filepath.Join(t.TempDir(), "nope"))
	if err != nil || len(found) != 0 {
		t.Fatalf("found=%v err=%v", found, err)
	}
}

func TestDiscoverRejectsBadManifest(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, filepath.Join(root, "broken"), "version: 1\n") // no name, no command
	_, err := Discover(root)
	if err == nil {
		t.Fatal("expected error for manifest without name/command")
	}
}
