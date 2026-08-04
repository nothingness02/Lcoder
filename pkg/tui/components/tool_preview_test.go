package components

import (
	"fmt"
	"strings"
	"testing"
)

func TestEditDiffStats(t *testing.T) {
	args := `{"path":"a.go","edits":[{"oldText":"foo\nbar","newText":"baz"},{"oldText":"x","newText":"y\nz"}]}`
	added, removed := EditDiffStats(args)
	if added != 3 || removed != 3 {
		t.Fatalf("EditDiffStats = +%d -%d, want +3 -3", added, removed)
	}
	if added, removed := EditDiffStats(`not json`); added != 0 || removed != 0 {
		t.Fatalf("invalid args should give zero stats, got +%d -%d", added, removed)
	}
	if added, removed := EditDiffStats(`{"path":"x"}`); added != 0 || removed != 0 {
		t.Fatalf("missing edits should give zero stats, got +%d -%d", added, removed)
	}
}

func TestEditDiffRows(t *testing.T) {
	args := `{"path":"a.go","edits":[{"oldText":"foo","newText":"baz"}]}`
	rows := EditDiffRows(args, 0, "ctrl+o")
	plain := stripANSI(strings.Join(rows, "\n"))
	if !strings.Contains(plain, "- foo") || !strings.Contains(plain, "+ baz") {
		t.Fatalf("expected diff rows, got %q", plain)
	}
	if got := EditDiffRows(`{"path":"x"}`, 0, ""); got != nil {
		t.Fatalf("expected nil rows for missing edits, got %v", got)
	}
}

func TestEditDiffRowsSharedBudget(t *testing.T) {
	// Two edits, each a 2-line change: with maxLines=3 the second edit's
	// changes must surface in the shared truncation footer.
	args := `{"path":"a.go","edits":[{"oldText":"a\nb","newText":"c\nd"},{"oldText":"e","newText":"f"}]}`
	rows := EditDiffRows(args, 3, "ctrl+o")
	if len(rows) != 4 {
		t.Fatalf("expected 3 rows + footer, got %d:\n%s", len(rows), stripANSI(strings.Join(rows, "\n")))
	}
	footer := stripANSI(rows[len(rows)-1])
	if !strings.Contains(footer, "more changes hidden") || !strings.Contains(footer, "ctrl+o to expand") {
		t.Fatalf("expected shared-budget footer, got %q", footer)
	}
}

func TestWriteContentRows(t *testing.T) {
	var lines []string
	for i := 1; i <= 15; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	rows := WriteContentRows(strings.Join(lines, "\n")+"\n", "main.go", 10, "ctrl+o")
	if len(rows) != 11 {
		t.Fatalf("expected 10 rows + hint, got %d", len(rows))
	}
	plain := stripANSI(rows[0])
	if !strings.HasPrefix(plain, "   1  ") {
		t.Fatalf("expected 4-wide gutter, got %q", plain)
	}
	if hint := stripANSI(rows[10]); !strings.Contains(hint, "+5 more") || !strings.Contains(hint, "ctrl+o to expand") {
		t.Fatalf("expected truncation hint, got %q", hint)
	}
	if got := WriteContentRows("", "main.go", 10, ""); got != nil {
		t.Fatalf("empty content should give nil rows, got %v", got)
	}
	// Unknown extension still renders (unstyled fallback) with gutters.
	plainRows := WriteContentRows("hello", "noext", 10, "")
	if len(plainRows) != 1 || !strings.Contains(stripANSI(plainRows[0]), "hello") {
		t.Fatalf("expected fallback plain row, got %v", plainRows)
	}
}

func TestCompactToolPreviewReadIsEmpty(t *testing.T) {
	out := compactToolPreview("read", `{"path":"a.go"}`, nil, "1\tfoo\n2\tbar", false, 80)
	if out != "" {
		t.Fatalf("read compact preview must be empty, got %q", out)
	}
}

func TestCompactToolPreviewBashTail(t *testing.T) {
	var lines []string
	for i := 1; i <= 6; i++ {
		lines = append(lines, fmt.Sprintf("out%d", i))
	}
	out := compactToolPreview("bash", `{"command":"ls"}`, nil, strings.Join(lines, "\n"), false, 80)
	plain := stripANSI(out)
	if !strings.Contains(plain, "out4") || !strings.Contains(plain, "out6") {
		t.Fatalf("expected last 3 lines, got %q", plain)
	}
	if strings.Contains(plain, "out2") {
		t.Fatalf("head lines should be elided, got %q", plain)
	}
	if !strings.Contains(plain, "+3 more") {
		t.Fatalf("expected elision marker, got %q", plain)
	}
}

func TestCompactToolPreviewEditDiff(t *testing.T) {
	args := `{"path":"a.go","edits":[{"oldText":"foo","newText":"bar"}]}`
	out := compactToolPreview("edit", args, nil, "Applied 1 edit(s) to a.go", false, 80)
	plain := stripANSI(out)
	if !strings.Contains(plain, "- foo") || !strings.Contains(plain, "+ bar") {
		t.Fatalf("expected edit diff preview, got %q", plain)
	}
}

func TestCompactToolPreviewWriteNewFile(t *testing.T) {
	args := `{"path":"a.go","content":"package main\n\nfunc main() {}"}`
	out := compactToolPreview("write", args, nil, "Wrote 30 characters to a.go", false, 80)
	plain := stripANSI(out)
	if !strings.Contains(plain, "package main") || !strings.Contains(plain, "   1  ") {
		t.Fatalf("expected highlighted content preview with gutter, got %q", plain)
	}
}

func TestCompactToolPreviewWriteOverwriteDiff(t *testing.T) {
	args := `{"path":"a.go","content":"line1\nline2"}`
	details := map[string]any{"old_content": "line1\nold2"}
	out := compactToolPreview("write", args, details, "Wrote 11 characters to a.go", false, 80)
	plain := stripANSI(out)
	if !strings.Contains(plain, "- old2") || !strings.Contains(plain, "+ line2") {
		t.Fatalf("expected overwrite diff preview, got %q", plain)
	}
}

func TestCompactToolPreviewErrorFallback(t *testing.T) {
	out := compactToolPreview("read", `{"path":"a.go"}`, nil, "open a.go: no such file", true, 80)
	if !strings.Contains(out, "no such file") {
		t.Fatalf("error text must stay visible, got %q", out)
	}
}
