package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/tui/components"
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

func TestChipForTool(t *testing.T) {
	cases := []struct {
		name   string
		tool   string
		result models.ToolExecutionResult
		want   string
	}{
		{"bash lines", "bash", models.NewToolExecutionResultText("a\nb\nc"), "3 lines"},
		{"grep matches", "grep", models.ToolExecutionResult{
			Content: []models.ContentPart{models.TextContent{Text: "x"}},
			Details: map[string]any{"matches": 42},
		}, "42 matches"},
		{"find files", "find", models.ToolExecutionResult{
			Content: []models.ContentPart{models.TextContent{Text: "x"}},
			Details: map[string]any{"matches": 7},
		}, "7 files"},
		{"edit edits", "edit", models.ToolExecutionResult{
			Content: []models.ContentPart{models.TextContent{Text: "x"}},
			Details: map[string]any{"edits": 2},
		}, "2 edits"},
		{"unknown tool no chip", "web", models.NewToolExecutionResultText("a"), ""},
	}
	for _, c := range cases {
		if got := chipForTool(c.tool, c.result); got != c.want {
			t.Errorf("%s: chipForTool = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestToolResultBashShowsCommandLine(t *testing.T) {
	comp := components.NewToolResultComponent("c1", "bash", `{"command":"ls -la"}`, "total 3", false, false, time.Now(), 0)
	out := comp.Render(80, false)
	if !strings.Contains(out, "$ ls -la") {
		t.Fatalf("bash body should lead with the command line, got:\n%s", out)
	}
}
