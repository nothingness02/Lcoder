package components

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
