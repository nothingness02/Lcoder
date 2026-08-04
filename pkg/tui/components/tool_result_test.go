package components

import (
	"strings"
	"testing"
	"time"
)

func TestToolResultComponentCompact(t *testing.T) {
	comp := NewToolResultComponent("t1", "bash", `{"command":"ls"}`, "a.go\nb.go", false, false, time.Time{}, 200*time.Millisecond)
	out := comp.Render(40, false)
	if !strings.Contains(out, "Running a command") {
		t.Fatalf("missing label: %q", out)
	}
	if !strings.Contains(out, "a.go") {
		t.Fatalf("missing preview: %q", out)
	}
}

func TestToolResultComponentExpanded(t *testing.T) {
	comp := NewToolResultComponent("t1", "bash", `{"command":"ls"}`, "a.go\nb.go", false, false, time.Time{}, 200*time.Millisecond)
	out := comp.Render(40, true)
	if !strings.Contains(out, "Arguments:") {
		t.Fatalf("missing arguments section: %q", out)
	}
	if !strings.Contains(out, "Output:") {
		t.Fatalf("missing output section: %q", out)
	}
}

func TestToolResultComponentError(t *testing.T) {
	comp := NewToolResultComponent("t1", "bash", `{"command":"ls"}`, "not found", true, false, time.Time{}, 0)
	out := comp.Render(40, false)
	if !strings.Contains(out, "✗") {
		t.Fatalf("expected error icon: %q", out)
	}
}

func TestToolResultComponentRunning(t *testing.T) {
	comp := NewToolResultComponent("t1", "bash", `{"command":"ls"}`, "", false, true, time.Now(), 0)
	out := comp.Render(40, false)
	if !strings.Contains(out, "Running a command") {
		t.Fatalf("missing label: %q", out)
	}
}

func TestToolResultComponentToggleExpanded(t *testing.T) {
	comp := NewToolResultComponent("t1", "bash", `{"command":"ls"}`, "a.go", false, false, time.Time{}, 0)
	if comp.expanded {
		t.Fatal("expected collapsed by default")
	}
	updated, _ := comp.Update(ToggleExpandedMsg{})
	tr := updated.(*ToolResultComponent)
	if !tr.expanded {
		t.Fatal("expected expanded after toggle")
	}
}

func TestToolResultComponentEditCollapsedDiff(t *testing.T) {
	args := `{"path":"main.go","edits":[{"oldText":"foo","newText":"baz"}]}`
	comp := NewToolResultComponent("t1", "edit", args, "Applied 1 edit(s) to main.go", false, false, time.Time{}, 0)
	out := comp.Render(80, false)
	if !strings.Contains(out, "- foo") || !strings.Contains(out, "+ baz") {
		t.Fatalf("collapsed edit should show the diff, got:\n%s", out)
	}
}

func TestToolResultComponentReadCollapsedNoBody(t *testing.T) {
	comp := NewToolResultComponent("t1", "read", `{"path":"main.go"}`, "1\tpackage main\n2\timport x", false, false, time.Time{}, 0)
	out := comp.Render(80, false)
	if strings.Contains(out, "package main") {
		t.Fatalf("collapsed read should have no body, got:\n%s", out)
	}
	// Expanded view still shows the full content.
	expanded := comp.Render(80, true)
	if !strings.Contains(expanded, "package main") {
		t.Fatalf("expanded read should show content, got:\n%s", expanded)
	}
}

func TestToolResultComponentWriteNewFilePreview(t *testing.T) {
	args := `{"path":"main.go","content":"package main\n\nfunc main() {}"}`
	comp := NewToolResultComponent("t1", "write", args, "Wrote 30 characters to main.go", false, false, time.Time{}, 0)
	out := stripANSI(comp.Render(80, false))
	if !strings.Contains(out, "package main") || !strings.Contains(out, "   1  ") {
		t.Fatalf("collapsed write should preview content with gutter, got:\n%s", out)
	}
}

func TestToolResultComponentWriteOverwriteDiff(t *testing.T) {
	args := `{"path":"main.go","content":"line1\nline2"}`
	comp := NewToolResultComponent("t1", "write", args, "Wrote 11 characters to main.go", false, false, time.Time{}, 0)
	comp.SetToolDetails(map[string]any{"old_content": "line1\nold2"})
	out := comp.Render(80, false)
	if !strings.Contains(out, "- old2") || !strings.Contains(out, "+ line2") {
		t.Fatalf("collapsed write-overwrite should show diff, got:\n%s", out)
	}
	// Without details (restored session) it degrades to the content preview.
	comp2 := NewToolResultComponent("t2", "write", args, "Wrote 11 characters to main.go", false, false, time.Time{}, 0)
	out2 := comp2.Render(80, false)
	if !strings.Contains(out2, "line1") || strings.Contains(out2, "- old2") {
		t.Fatalf("write without details should fall back to content preview, got:\n%s", out2)
	}
}

func TestToolResultComponentBashTailPreview(t *testing.T) {
	comp := NewToolResultComponent("t1", "bash", `{"command":"ls"}`, "l1\nl2\nl3\nl4\nl5", false, false, time.Time{}, 0)
	out := comp.Render(80, false)
	if !strings.Contains(out, "l5") || !strings.Contains(out, "l3") {
		t.Fatalf("bash preview should show output tail, got:\n%s", out)
	}
	if strings.Contains(out, "  │ l1") {
		t.Fatalf("bash preview should elide head lines, got:\n%s", out)
	}
}
