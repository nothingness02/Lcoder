package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "lcoder-builtin-*")
	if err != nil {
		t.Fatalf("mkdirtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestRead(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(path, []byte("line1\nline2\nline3"), 0o644); err != nil {
		t.Fatal(err)
	}

	read := NewRead(dir)
	result, err := read.Execute(context.Background(), "call_1", map[string]any{
		"path":   "hello.txt",
		"offset": float64(2),
		"limit":  float64(1),
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(result.Content))
	}
	text := result.Content[0].(models.TextContent)
	if text.Text != "line2" {
		t.Fatalf("expected line2, got %q", text.Text)
	}
}

func TestWrite(t *testing.T) {
	dir := tempDir(t)
	write := NewWrite(dir)
	result, err := write.Execute(context.Background(), "call_1", map[string]any{
		"path":    "subdir/file.txt",
		"content": "hello world",
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "subdir", "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello world" {
		t.Fatalf("expected 'hello world', got %q", string(data))
	}
	if !strings.Contains(result.Content[0].(models.TextContent).Text, "11 characters") {
		t.Fatalf("unexpected result text: %v", result.Content[0])
	}
}

func TestEdit(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte("foo bar baz"), 0o644); err != nil {
		t.Fatal(err)
	}

	edit := NewEdit(dir)
	_, err := edit.Execute(context.Background(), "call_1", map[string]any{
		"path": "main.go",
		"edits": []any{
			map[string]any{"oldText": "bar", "newText": "qux"},
		},
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "foo qux baz" {
		t.Fatalf("expected 'foo qux baz', got %q", string(data))
	}
}

func TestLs(t *testing.T) {
	dir := tempDir(t)
	_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte(""), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "b.go"), []byte(""), 0o644)

	ls := NewLs(dir)
	result, err := ls.Execute(context.Background(), "call_1", map[string]any{})
	if err != nil {
		t.Fatalf("ls: %v", err)
	}
	text := result.Content[0].(models.TextContent).Text
	if !strings.Contains(text, "a.go") || !strings.Contains(text, "b.go") {
		t.Fatalf("expected a.go and b.go in output, got %q", text)
	}
}

func TestGrep(t *testing.T) {
	dir := tempDir(t)
	_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte("func Foo() {}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "b.go"), []byte("func Bar() {}\n"), 0o644)

	grep := NewGrep(dir)
	result, err := grep.Execute(context.Background(), "call_1", map[string]any{
		"pattern": "Foo",
		"glob":    "*.go",
	})
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	text := result.Content[0].(models.TextContent).Text
	if !strings.Contains(text, "a.go:1:func Foo() {}") {
		t.Fatalf("expected match, got %q", text)
	}
	if strings.Contains(text, "Bar") {
		t.Fatal("unexpected Bar match")
	}
}

func TestFind(t *testing.T) {
	dir := tempDir(t)
	_ = os.WriteFile(filepath.Join(dir, "a_test.go"), []byte(""), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "main.go"), []byte(""), 0o644)

	find := NewFind(dir)
	result, err := find.Execute(context.Background(), "call_1", map[string]any{
		"pattern": "*_test.go",
	})
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	text := result.Content[0].(models.TextContent).Text
	if !strings.Contains(text, "a_test.go") {
		t.Fatalf("expected a_test.go, got %q", text)
	}
	if strings.Contains(text, "main.go") {
		t.Fatal("unexpected main.go match")
	}
}

func TestBash(t *testing.T) {
	bash := NewBash(".")
	result, err := bash.Execute(context.Background(), "call_1", map[string]any{
		"command": "go version",
	})
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	text := result.Content[0].(models.TextContent).Text
	if !strings.HasPrefix(text, "go version") {
		t.Fatalf("expected go version output, got %q", text)
	}
}

func TestReadDefaultLimitTruncates(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "big.txt")
	var sb strings.Builder
	for i := 0; i < defaultReadLines+50; i++ {
		sb.WriteString(fmt.Sprintf("line %d\n", i+1))
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	read := NewRead(dir)
	result, err := read.Execute(context.Background(), "call_1", map[string]any{
		"path": "big.txt",
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	text := result.Content[0].(models.TextContent).Text
	if !strings.Contains(text, "line 1") {
		t.Fatal("expected first line in truncated output")
	}
	if strings.Contains(text, fmt.Sprintf("line %d", defaultReadLines+50)) {
		t.Fatal("expected output to be truncated before the last line")
	}
	if !strings.Contains(text, "[truncated:") {
		t.Fatal("expected truncation warning")
	}
}

func TestReadRejectsOversizedFile(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "huge.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxReadFileSizeBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}

	read := NewRead(dir)
	_, err := read.Execute(context.Background(), "call_1", map[string]any{
		"path": "huge.txt",
	})
	if err == nil {
		t.Fatal("expected error for oversized file")
	}
}

func TestGrepLimitsMatches(t *testing.T) {
	dir := tempDir(t)
	for i := 0; i < 10; i++ {
		path := filepath.Join(dir, fmt.Sprintf("f%d.txt", i))
		var sb strings.Builder
		for j := 0; j < 100; j++ {
			sb.WriteString("needle here\n")
		}
		if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	grep := NewGrep(dir)
	result, err := grep.Execute(context.Background(), "call_1", map[string]any{
		"pattern": "needle",
	})
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	text := result.Content[0].(models.TextContent).Text
	if !strings.Contains(text, "[truncated:") {
		t.Fatal("expected truncation warning")
	}
	if result.Details["matches"] != maxGrepMatches {
		t.Fatalf("expected %d matches, got %v", maxGrepMatches, result.Details["matches"])
	}
}

func TestFindLimitsMatches(t *testing.T) {
	dir := tempDir(t)
	for i := 0; i < maxFindMatches+10; i++ {
		path := filepath.Join(dir, fmt.Sprintf("match_%d.txt", i))
		if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	find := NewFind(dir)
	result, err := find.Execute(context.Background(), "call_1", map[string]any{
		"pattern": "match_*.txt",
	})
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	text := result.Content[0].(models.TextContent).Text
	if !strings.Contains(text, "[truncated:") {
		t.Fatal("expected truncation warning")
	}
	if result.Details["matches"] != maxFindMatches {
		t.Fatalf("expected %d matches, got %v", maxFindMatches, result.Details["matches"])
	}
}

func TestLsLimitsEntries(t *testing.T) {
	dir := tempDir(t)
	for i := 0; i < maxLsEntries+10; i++ {
		path := filepath.Join(dir, fmt.Sprintf("file_%03d.txt", i))
		if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ls := NewLs(dir)
	result, err := ls.Execute(context.Background(), "call_1", map[string]any{})
	if err != nil {
		t.Fatalf("ls: %v", err)
	}
	text := result.Content[0].(models.TextContent).Text
	if !strings.Contains(text, "[truncated:") {
		t.Fatal("expected truncation warning")
	}
	if result.Details["count"] != maxLsEntries {
		t.Fatalf("expected %d entries, got %v", maxLsEntries, result.Details["count"])
	}
}
