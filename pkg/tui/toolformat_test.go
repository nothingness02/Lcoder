package tui

import (
	"strings"
	"testing"
)

func TestFormatToolSummary(t *testing.T) {
	results := []toolResultEntry{{isError: false}, {isError: true}, {isError: false}}
	out := formatToolSummary(results)
	if out == "" {
		t.Fatal("empty summary")
	}
	// Icon and count must be separated by a space.
	if strings.Contains(out, "✓2") || strings.Contains(out, "✗1") {
		t.Fatalf("icon and count must be spaced: %q", out)
	}
	if !strings.Contains(out, "3 tools") {
		t.Fatalf("expected '3 tools', got %q", out)
	}
}

func TestFormatToolSummarySingular(t *testing.T) {
	out := formatToolSummary([]toolResultEntry{{isError: false}})
	if !strings.Contains(out, "1 tool") {
		t.Fatalf("expected singular '1 tool', got %q", out)
	}
}

func TestFormatToolSummaryAllSuccess(t *testing.T) {
	out := formatToolSummary([]toolResultEntry{{isError: false}, {isError: false}})
	if !strings.Contains(out, "2 tools") {
		t.Fatalf("expected tool count, got %q", out)
	}
	// With no errors only the success icon should appear.
	if strings.Contains(out, "✗") {
		t.Fatalf("unexpected error icon in all-success summary: %q", out)
	}
}
