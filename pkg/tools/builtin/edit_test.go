package builtin

import (
	"context"
	"os"
	"path/filepath"
	"testing"

)

func TestEdit_DryRunFailureLeavesFileUnchanged(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "main.go")
	original := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	edit := NewEdit(dir).(*Edit)

	_, err := edit.Execute(context.Background(), "call_1", map[string]any{
		"path": "main.go",
		"edits": []any{
			map[string]any{"oldText": "THIS DOES NOT EXIST", "newText": "x"},
		},
	})
	if err == nil {
		t.Fatal("expected error for non-matching edit")
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("file was modified despite dry-run failure: %q", string(data))
	}
}

func TestEdit_CommitSuccess(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "main.go")
	original := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	edit := NewEdit(dir).(*Edit)

	res, err := edit.Execute(context.Background(), "call_1", map[string]any{
		"path": "main.go",
		"edits": []any{
			map[string]any{"oldText": "func main() {}", "newText": "func main() { println(\"hi\") }"},
		},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	want := "package main\n\nfunc main() { println(\"hi\") }\n"
	if string(data) != want {
		t.Fatalf("file = %q, want %q", string(data), want)
	}
	if _, err := os.Stat(target + backupSuffix); !os.IsNotExist(err) {
		t.Fatal("backup file should be removed after successful commit")
	}
	if res.Text() == "" {
		t.Fatal("expected non-empty result text")
	}
}
