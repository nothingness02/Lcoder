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
