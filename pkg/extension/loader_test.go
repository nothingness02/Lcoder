package extension

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoaderInstallLocal(t *testing.T) {
	root, err := os.MkdirTemp("", "lcoder-ext-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	src, err := os.MkdirTemp("", "lcoder-ext-src-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(src) })

	if err := os.WriteFile(filepath.Join(src, "extension.yaml"), []byte("name: test-ext\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	loader := NewLoader(root)
	dir, err := loader.Install("test-ext", src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "extension.yaml")); err != nil {
		t.Fatal(err)
	}

	names, err := loader.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "test-ext" {
		t.Fatalf("unexpected extensions: %v", names)
	}

	if err := loader.Uninstall("test-ext"); err != nil {
		t.Fatal(err)
	}
	names, err = loader.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 0 {
		t.Fatalf("expected no extensions, got %v", names)
	}
}

func TestLoadPackage(t *testing.T) {
	dir, err := os.MkdirTemp("", "lcoder-pkg-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	meta := `name: my-pkg
version: 1.0.0
author: test
`
	if err := os.WriteFile(filepath.Join(dir, "lcoder-package.yaml"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}

	pkg, err := LoadPackage(dir)
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Info.Name != "my-pkg" {
		t.Fatalf("unexpected name: %s", pkg.Info.Name)
	}
	if !pkg.HasAgents() {
		t.Fatal("expected agents dir")
	}
}
