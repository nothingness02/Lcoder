package components

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
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

func TestBuildEditDiff(t *testing.T) {
	args := `{"path":"main.go","edits":[{"oldText":"foo\nbar","newText":"baz"}]}`
	diff := buildEditDiff(args)
	if !strings.Contains(diff, "-foo") || !strings.Contains(diff, "-bar") || !strings.Contains(diff, "+baz") {
		t.Fatalf("expected old/new diff markers, got %q", diff)
	}
	if got := buildEditDiff(`{"path":"x"}`); got != "" {
		t.Fatalf("expected empty for missing edits, got %q", got)
	}
	if got := buildEditDiff(`not json`); got != "" {
		t.Fatalf("expected empty for invalid json, got %q", got)
	}
}

func TestFormatExpandedToolResultEditDiff(t *testing.T) {
	args := `{"path":"main.go","edits":[{"oldText":"foo","newText":"baz"}]}`
	out := formatExpandedToolResult("edit", args, false, "Edited main.go", 0, false, 80)
	if !strings.Contains(out, "Changes:") {
		t.Fatalf("expected Changes section, got %q", out)
	}
	if !strings.Contains(out, "-foo") || !strings.Contains(out, "+baz") {
		t.Fatalf("expected colored diff lines, got %q", out)
	}
	if strings.Contains(out, "Arguments:") {
		t.Fatalf("edit should not echo raw JSON arguments, got %q", out)
	}
}

func TestToolPreviewHeadTail(t *testing.T) {
	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, fmt.Sprintf("line%d", i))
	}
	out := collapseToolOutput(strings.Join(lines, "\n"), 2, 1, 80)
	if !strings.Contains(out, "line1") || !strings.Contains(out, "line2") {
		t.Fatalf("expected head lines, got %q", out)
	}
	if !strings.Contains(out, "line10") {
		t.Fatalf("expected tail line, got %q", out)
	}
	if !strings.Contains(out, "+7 more") {
		t.Fatalf("expected elision marker, got %q", out)
	}
	if strings.Contains(out, "line5") {
		t.Fatalf("middle lines should be elided, got %q", out)
	}
}

func TestCollapseToolOutputCharBudget(t *testing.T) {
	// Three 30-cell lines at width 30: the budget is 3 * (30-6) = 72 cells,
	// so each sampled line is clipped to its share instead of filling rows.
	line := strings.Repeat("x", 30)
	out := collapseToolOutput(line+"\n"+line+"\n"+line, 2, 1, 30)
	for _, ln := range strings.Split(out, "\n") {
		if lipgloss.Width(ln) > 24 {
			t.Fatalf("line exceeds char budget share: %q (width %d)", ln, lipgloss.Width(ln))
		}
	}
}

func TestCollapseToolOutputHugeSingleLine(t *testing.T) {
	out := collapseToolOutput(strings.Repeat("y", 5000), 2, 1, 40)
	if lipgloss.Width(out) > 40 {
		t.Fatalf("single huge line not clipped to width: width %d", lipgloss.Width(out))
	}
	if !strings.HasSuffix(out, "…") {
		t.Fatalf("expected ellipsis on clipped line, got %q", out[len(out)-20:])
	}
}

func TestCollapseToolOutputEmpty(t *testing.T) {
	if out := collapseToolOutput("  \n  ", 2, 1, 80); out != "" {
		t.Fatalf("expected empty output, got %q", out)
	}
}
