package builtin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestEditCRLFMatchedInLFView(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "win.go")
	original := "package main\r\n\r\nfunc main() {}\r\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	edit := NewEdit(dir).(*Edit)
	// oldText uses the LF view shown by the read tool, including a multi-line span.
	_, err := edit.Execute(context.Background(), "call_1", map[string]any{
		"path": "win.go",
		"edits": []any{
			map[string]any{"oldText": "package main\n\nfunc main() {}", "newText": "package main\n\nfunc main() { run() }"},
		},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	want := "package main\r\n\r\nfunc main() { run() }\r\n"
	if string(data) != want {
		t.Fatalf("file = %q, want %q (CRLF must be preserved, not doubled)", string(data), want)
	}
}

func TestEditMixedLineEndingsMatchRawBytes(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "mixed.txt")
	// Mixed: \r\n coexists with a bare \n, so the raw-byte path is used and
	// oldText must reproduce carriage returns exactly (as read shows `\r`).
	original := "a\r\nb\nc\r\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	edit := NewEdit(dir).(*Edit)
	_, err := edit.Execute(context.Background(), "call_1", map[string]any{
		"path": "mixed.txt",
		"edits": []any{
			map[string]any{"oldText": "a\r\nb", "newText": "x"},
		},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "x\nc\r\n" {
		t.Fatalf("file = %q, want %q", string(data), "x\nc\r\n")
	}
}

func TestEditNotFoundSuggestsReread(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(target, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	edit := NewEdit(dir).(*Edit)
	_, err := edit.Execute(context.Background(), "call_1", map[string]any{
		"path":  "a.txt",
		"edits": []any{map[string]any{"oldText": "missing", "newText": "x"}},
	})
	if err == nil || !strings.Contains(err.Error(), "read the file again") {
		t.Fatalf("error should instruct re-reading the file, got %v", err)
	}
}

func TestEditNotUniqueReportsCountAndReplaceAll(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(target, []byte("x\nx\nx\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	edit := NewEdit(dir).(*Edit)
	_, err := edit.Execute(context.Background(), "call_1", map[string]any{
		"path":  "a.txt",
		"edits": []any{map[string]any{"oldText": "x", "newText": "y"}},
	})
	if err == nil {
		t.Fatal("expected non-unique error")
	}
	if !strings.Contains(err.Error(), "3 occurrences") || !strings.Contains(err.Error(), "replaceAll") {
		t.Fatalf("error should report the count and the replaceAll escape hatch, got %v", err)
	}

	// replaceAll replaces every occurrence.
	_, err = edit.Execute(context.Background(), "call_2", map[string]any{
		"path":  "a.txt",
		"edits": []any{map[string]any{"oldText": "x", "newText": "y", "replaceAll": true}},
	})
	if err != nil {
		t.Fatalf("replaceAll: %v", err)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "y\ny\ny\n" {
		t.Fatalf("file = %q, want all occurrences replaced", string(data))
	}
}

func TestEditNoOpRejectedBeforeIO(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(target, []byte("same\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	edit := NewEdit(dir).(*Edit)
	_, err := edit.Execute(context.Background(), "call_1", map[string]any{
		"path":  "a.txt",
		"edits": []any{map[string]any{"oldText": "same", "newText": "same"}},
	})
	if err == nil || !strings.Contains(err.Error(), "identical") {
		t.Fatalf("expected no-op rejection, got %v", err)
	}
}

func TestEditRejectsNonUTF8(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "bin.txt")
	if err := os.WriteFile(target, []byte{'a', 0xff, 0xfe, 'b'}, 0o644); err != nil {
		t.Fatal(err)
	}

	edit := NewEdit(dir).(*Edit)
	_, err := edit.Execute(context.Background(), "call_1", map[string]any{
		"path":  "bin.txt",
		"edits": []any{map[string]any{"oldText": "a", "newText": "b"}},
	})
	if err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("expected UTF-8 rejection, got %v", err)
	}
	data, _ := os.ReadFile(target)
	if len(data) != 4 {
		t.Fatal("non-UTF-8 file bytes must be left untouched")
	}
}
