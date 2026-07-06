package tui

import (
	"strings"
	"testing"
	"time"
)

func TestFriendlyToolLabel(t *testing.T) {
	if got := friendlyToolLabel("bash"); got != "Running a command" {
		t.Fatalf("bash label = %q", got)
	}
	if got := friendlyToolLabel("unknown_tool"); got != "unknown_tool" {
		t.Fatalf("unknown label = %q, want passthrough", got)
	}
}

func TestToolKeyArg(t *testing.T) {
	if got := toolKeyArg("bash", `{"command":"go test ./..."}`); got != "go test ./..." {
		t.Fatalf("bash keyarg = %q", got)
	}
	if got := toolKeyArg("read", `{"path":"main.go"}`); got != "main.go" {
		t.Fatalf("read keyarg = %q", got)
	}
	if got := toolKeyArg("grep", `{"pattern":"TODO","path":"pkg"}`); got != "TODO, pkg" {
		t.Fatalf("grep keyarg = %q", got)
	}
}

func TestFormatCompactToolResult(t *testing.T) {
	out := formatCompactToolResult("bash", `{"command":"ls"}`, false, "ok", 1200*time.Millisecond, false)
	if out == "" {
		t.Fatal("empty compact result")
	}
	if !strings.Contains(out, "Running a command") {
		t.Fatalf("expected friendly label, got %q", out)
	}
	// Preview lines should be visually subordinate to the header.
	if !strings.Contains(out, "  │ ok") {
		t.Fatalf("expected preview line with subordinate marker, got %q", out)
	}
}

func TestFormatCompactToolResultRunning(t *testing.T) {
	out := formatCompactToolResult("bash", `{"command":"ls"}`, false, "", 250*time.Millisecond, true)
	if strings.Contains(out, "✓") {
		t.Fatalf("running tool should not show success icon: %q", out)
	}
	if !strings.Contains(out, "Running a command") {
		t.Fatalf("running tool should still show label: %q", out)
	}
}

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
