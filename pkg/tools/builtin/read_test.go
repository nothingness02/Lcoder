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

// writeLargeFile writes a file larger than maxReadFileSizeBytes whose lines
// are individually addressable ("line 000001" ...).
func writeLargeFile(t *testing.T, dir string) (string, int) {
	t.Helper()
	var sb strings.Builder
	lines := 0
	for sb.Len() <= maxReadFileSizeBytes {
		lines++
		fmt.Fprintf(&sb, "line %06d\n", lines)
	}
	path := filepath.Join(dir, "large.txt")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path, lines
}

func TestReadLargeFileRequiresWindow(t *testing.T) {
	dir := tempDir(t)
	writeLargeFile(t, dir)

	read := NewRead(dir)
	_, err := read.Execute(context.Background(), "call_1", map[string]any{"path": "large.txt"})
	if err == nil {
		t.Fatal("expected error for large file without offset/limit")
	}
	if !strings.Contains(err.Error(), "offset/limit") {
		t.Fatalf("error should teach the windowed-read retry, got %q", err.Error())
	}
}

func TestReadLargeFileWindowSucceeds(t *testing.T) {
	dir := tempDir(t)
	_, totalLines := writeLargeFile(t, dir)

	read := NewRead(dir)
	result, err := read.Execute(context.Background(), "call_1", map[string]any{
		"path":   "large.txt",
		"offset": float64(100),
		"limit":  float64(10),
	})
	if err != nil {
		t.Fatalf("windowed read of large file should succeed: %v", err)
	}
	text := result.Content[0].(models.TextContent).Text
	if !strings.HasPrefix(text, "line 000100") {
		t.Fatalf("expected window to start at line 100, got %q", text[:64])
	}
	// A trailing newline splits into one extra empty element, so the reported
	// total is the file's line count plus one.
	if !strings.Contains(text, fmt.Sprintf("of %d", totalLines+1)) {
		t.Fatalf("expected truncation notice with total line count, got %q", text)
	}
}

func TestReadOffsetBeyondEndOfFile(t *testing.T) {
	dir := tempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "small.txt"), []byte("a\nb\nc"), 0o644); err != nil {
		t.Fatal(err)
	}

	read := NewRead(dir)
	_, err := read.Execute(context.Background(), "call_1", map[string]any{
		"path":   "small.txt",
		"offset": float64(99),
	})
	if err == nil {
		t.Fatal("expected error for offset beyond end of file")
	}
	if !strings.Contains(err.Error(), "3 lines total") {
		t.Fatalf("error should state total line count, got %q", err.Error())
	}
}

func TestReadPureCRLFShownAsLF(t *testing.T) {
	dir := tempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "win.txt"), []byte("a\r\nb\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	read := NewRead(dir)
	result, err := read.Execute(context.Background(), "call_1", map[string]any{"path": "win.txt"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	text := result.Content[0].(models.TextContent).Text
	if strings.Contains(text, "\r") {
		t.Fatalf("pure CRLF file should be shown with LF line endings, got %q", text)
	}
	if !strings.Contains(text, "a\nb") {
		t.Fatalf("unexpected content: %q", text)
	}
}

func TestReadMixedLineEndingsShowLiteralCR(t *testing.T) {
	dir := tempDir(t)
	// \r\n coexisting with a bare \n -> mixed -> carriage returns made visible.
	if err := os.WriteFile(filepath.Join(dir, "mixed.txt"), []byte("a\r\nb\nc"), 0o644); err != nil {
		t.Fatal(err)
	}

	read := NewRead(dir)
	result, err := read.Execute(context.Background(), "call_1", map[string]any{"path": "mixed.txt"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	text := result.Content[0].(models.TextContent).Text
	if !strings.Contains(text, `a\r`) {
		t.Fatalf("carriage return should be shown literally as \r, got %q", text)
	}
}
