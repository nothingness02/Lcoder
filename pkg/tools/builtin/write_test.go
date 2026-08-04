package builtin

import (
	"context"
	"os"
	"path/filepath"
	"testing"

)

func TestWrite_BackupAndOverwrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	write := NewWrite(dir).(*Write)

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
	if got, ok := res.Details["old_content"].(string); !ok || got != "old" {
		t.Fatalf("overwrite should ship old content in details, got %v", res.Details)
	}
}

func TestWrite_NewFileHasNoOldContentDetail(t *testing.T) {
	dir := t.TempDir()
	write := NewWrite(dir).(*Write)

	res, err := write.Execute(context.Background(), "call_1", map[string]any{
		"path":    "fresh.txt",
		"content": "hello",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, ok := res.Details["old_content"]; ok {
		t.Fatalf("new file must not carry old_content, got %v", res.Details)
	}
}

func TestWrite_OversizedOldContentOmitted(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "big.bin")
	big := make([]byte, maxWriteDiffOldSize+1)
	if err := os.WriteFile(target, big, 0o644); err != nil {
		t.Fatal(err)
	}

	write := NewWrite(dir).(*Write)
	res, err := write.Execute(context.Background(), "call_1", map[string]any{
		"path":    "big.bin",
		"content": "new",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, ok := res.Details["old_content"]; ok {
		t.Fatal("old_content beyond the size cap must be omitted from details")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("file = %q, want new", string(data))
	}
}

func TestWrite_FailureKeepsOriginal(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "readonly", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Remove write permission from both the file and its parent directory.
	// On POSIX the directory permission gate-keeps file creation/deletion;
	// file permission alone is not enough when the test runs as the owner.
	if err := os.Chmod(target, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(target), 0o555); err != nil {
		t.Fatal(err)
	}
	// Restore write permission so TempDir cleanup can remove the directory.
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(target), 0o755) })

	write := NewWrite(dir).(*Write)

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
