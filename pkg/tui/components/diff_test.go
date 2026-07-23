package components

import (
	"strings"
	"testing"
)

func TestParseDiff(t *testing.T) {
	lines := ParseDiff("+added\n-removed\n@@ hunk @@\ncontext")
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines, got %d", len(lines))
	}
	kinds := []string{"add", "remove", "header", "context"}
	for i, want := range kinds {
		if lines[i].Kind != want {
			t.Fatalf("line %d kind = %q, want %q", i, lines[i].Kind, want)
		}
	}
}

func TestRenderDiff(t *testing.T) {
	out := RenderDiff(ParseDiff("+new\n-old"), 80)
	if !strings.Contains(out, "+new") || !strings.Contains(out, "-old") {
		t.Fatalf("expected diff lines rendered, got %q", out)
	}
	if got := RenderDiff(nil, 80); !strings.Contains(got, "No diff") {
		t.Fatalf("expected empty placeholder, got %q", got)
	}
}
