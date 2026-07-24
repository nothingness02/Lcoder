package builtin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/models"
)

// These tests execute real shell commands through the local process layer
// (proc.go); they require `sh` on PATH, as the tool itself does.

func TestBashReturnsStructuredDetails(t *testing.T) {
	b := NewBash(t.TempDir()).(*Bash)

	res, err := b.Execute(context.Background(), "c1", map[string]any{
		"command": "echo hello; echo warn >&2; exit 42",
	})
	if err != nil {
		t.Fatalf("unexpected error for non-zero exit: %v", err)
	}

	if got := res.Details["stdout"]; got != "hello\n" {
		t.Fatalf("stdout detail = %q, want %q", got, "hello\n")
	}
	if got := res.Details["stderr"]; got != "warn\n" {
		t.Fatalf("stderr detail = %q, want %q", got, "warn\n")
	}
	if got := res.Details["exit_code"]; got != 42 {
		t.Fatalf("exit_code detail = %v, want 42", got)
	}
	if got := res.Details["timed_out"]; got != false {
		t.Fatalf("timed_out detail = %v, want false", got)
	}
}

func TestBashNonZeroExitReturnsBusinessResult(t *testing.T) {
	b := NewBash(t.TempDir()).(*Bash)

	res, err := b.Execute(context.Background(), "c1", map[string]any{
		"command": "echo boom >&2; exit 1",
	})
	if err != nil {
		t.Fatalf("expected no system error for non-zero exit: %v", err)
	}
	if res.Details["exit_code"] != 1 {
		t.Fatalf("exit_code = %v, want 1", res.Details["exit_code"])
	}
	if !res.IsError {
		t.Fatal("non-zero exit should flag the result as an error")
	}
}

func TestBashTimeoutMarksOutput(t *testing.T) {
	b := NewBash(t.TempDir()).(*Bash)

	res, err := b.Execute(context.Background(), "c1", map[string]any{
		"command": "echo partial; sleep 60",
		"timeout": 1,
	})
	if err != nil {
		t.Fatalf("expected no system error on timeout: %v", err)
	}
	txt := res.Content[0].(models.TextContent).Text
	if !strings.Contains(txt, "partial") || !strings.Contains(txt, "[command timed out]") {
		t.Fatalf("output = %q, want partial output plus timeout marker", txt)
	}
	if res.Details["timed_out"] != true {
		t.Fatalf("timed_out = %v, want true", res.Details["timed_out"])
	}
	if !res.IsError {
		t.Fatal("timeout should flag the result as an error")
	}
}

// TestBashTimeoutKillsProcessTree verifies the whole process tree is killed on
// timeout: the command backgrounds a grandchild that would write a marker file
// after 2s; if the tree kill works, the marker never appears.
func TestBashTimeoutKillsProcessTree(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	b := NewBash(dir).(*Bash)

	_, err := b.Execute(context.Background(), "c1", map[string]any{
		"command": "(sleep 2; touch marker) & sleep 60",
		"timeout": 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(marker); statErr == nil {
			t.Fatal("orphaned grandchild survived the timeout kill and wrote the marker")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestBashNonZeroExitSurfacesExitCode(t *testing.T) {
	b := NewBash(t.TempDir()).(*Bash)

	res, err := b.Execute(context.Background(), "c1", map[string]any{
		"command": "echo some output; exit 3",
	})
	if err != nil {
		t.Fatalf("expected no Go error for non-zero exit: %v", err)
	}
	if !res.IsError {
		t.Fatal("non-zero exit should flag the result as an error")
	}
	txt := res.Content[0].(models.TextContent).Text
	if !strings.Contains(txt, "[exit code: 3]") {
		t.Fatalf("expected exit code in model-visible text, got %q", txt)
	}
}

func TestBashLabelsStderr(t *testing.T) {
	b := NewBash(t.TempDir()).(*Bash)

	res, err := b.Execute(context.Background(), "c1", map[string]any{
		"command": "echo out; echo warn >&2",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	txt := res.Content[0].(models.TextContent).Text
	// Real shells end stdout with a newline, so the label separator produces a
	// blank line between stdout and the [stderr] block.
	if !strings.Contains(txt, "out\n\n[stderr]\nwarn") {
		t.Fatalf("expected labeled stderr, got %q", txt)
	}
	if res.IsError {
		t.Fatal("exit 0 should not be flagged as an error")
	}
}

func TestBashTruncatesLongOutput(t *testing.T) {
	b := NewBash(t.TempDir()).(*Bash)

	res, err := b.Execute(context.Background(), "c1", map[string]any{
		"command": "awk 'BEGIN{for(i=0;i<70000;i++)printf \"x\"}'",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	txt := res.Content[0].(models.TextContent).Text
	if !strings.Contains(txt, "[... truncated ") {
		t.Fatal("expected truncation marker in output")
	}
	if len([]rune(txt)) > maxBashOutputChars+64 {
		t.Fatalf("output not bounded: %d runes", len([]rune(txt)))
	}
	if res.Details["truncated"] != true {
		t.Fatalf("truncated detail = %v, want true", res.Details["truncated"])
	}
	// Head and tail are both preserved.
	if !strings.HasPrefix(txt, "xxx") || !strings.HasSuffix(txt, "xxx") {
		t.Fatal("expected head and tail of output to be preserved")
	}
}

func TestTruncateHeadTailKeepsShortInput(t *testing.T) {
	s, truncated := truncateHeadTail("short", 100)
	if truncated || s != "short" {
		t.Fatalf("short input changed: %q, %v", s, truncated)
	}
}

func TestTruncateHeadTailRuneSafe(t *testing.T) {
	s, truncated := truncateHeadTail(strings.Repeat("汉", 100), 40)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if !strings.Contains(s, "[... truncated 60 chars ...]") {
		t.Fatalf("unexpected marker: %q", s)
	}
}
