package builtin

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lcoder/lcoder/pkg/sandbox"
)

func TestWrite_BackupAndOverwrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	write := NewWrite(dir).(*Write)
	write.UseSandbox(sandbox.NewFakeSandbox())

	res, err := write.Execute(context.Background(), "call_1", map[string]any{
		"path":    "config.yaml",
		"content": "new",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("file = %q, want new", string(data))
	}
	if _, err := os.Stat(target + backupSuffix); !os.IsNotExist(err) {
		t.Fatal("backup should be removed after success")
	}
	if res.Text() == "" {
		t.Fatal("expected non-empty result")
	}
}

func TestWrite_FailureKeepsOriginal(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "readonly", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o444); err != nil {
		t.Fatal(err)
	}

	write := NewWrite(dir).(*Write)
	write.UseSandbox(sandbox.NewFakeSandbox())

	_, err := write.Execute(context.Background(), "call_1", map[string]any{
		"path":    "readonly/config.yaml",
		"content": "new",
	})
	if err == nil {
		t.Fatal("expected error writing to read-only file")
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old" {
		t.Fatalf("original file was mutated: %q", string(data))
	}
}
