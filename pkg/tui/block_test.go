package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRenderUserBlock(t *testing.T) {
	b := block{kind: BlockUser, raw: "hello world"}
	out := b.render(80, false)
	if !strings.Contains(out, "hello world") {
		t.Fatalf("user block missing text: %q", out)
	}
}

func TestRenderAssistantBlockMarkdown(t *testing.T) {
	b := block{kind: BlockAssistant, raw: "# Hi\n\ntext"}
	out := b.render(80, false)
	if strings.Contains(out, "# Hi") {
		t.Fatal("assistant block did not render markdown")
	}
}

func TestRenderToolBlockCompactVsExpanded(t *testing.T) {
	var lines []string
	for i := 1; i <= 20; i++ {
		lines = append(lines, fmt.Sprintf("line%d", i))
	}
	b := block{
		kind:       BlockTool,
		toolName:   "bash",
		toolArgs:   `{"command":"ls"}`,
		toolResult: strings.Join(lines, "\n"),
	}
	compact := b.render(80, false)
	expanded := b.render(80, true)
	if compact == expanded {
		t.Fatal("expanded should differ from compact")
	}
	// Compact shows only a few preview lines plus a "more" hint.
	if strings.Contains(compact, "line8") {
		t.Fatalf("compact should hide later lines: %q", compact)
	}
	// Expanded shows the complete output without head/tail elision.
	if !strings.Contains(expanded, "line20") {
		t.Fatalf("expanded should show the full output: %q", expanded)
	}
	if strings.Contains(expanded, "… +") {
		t.Fatalf("expanded should not elide middle lines: %q", expanded)
	}
}

func TestRenderExpandedToolBlockShowsFormattedArgs(t *testing.T) {
	b := block{
		kind:       BlockTool,
		toolName:   "bash",
		toolArgs:   `{"command":"ls -la","path":"."}`,
		toolResult: "ok",
	}
	expanded := b.render(80, true)
	if !strings.Contains(expanded, "Arguments:") {
		t.Fatalf("expanded should show Arguments section: %q", expanded)
	}
	if !strings.Contains(expanded, "command") || !strings.Contains(expanded, "path") {
		t.Fatalf("expanded should show formatted arg keys: %q", expanded)
	}
	// Pretty-printed JSON uses indentation, so colons should be followed by a space.
	if !strings.Contains(expanded, "command\": \"ls -la\"") {
		t.Fatalf("expanded should show pretty-printed arg value: %q", expanded)
	}
}

func TestRenderRunningToolBlockNoSuccessIcon(t *testing.T) {
	b := block{kind: BlockTool, toolName: "bash", toolArgs: `{"command":"ls"}`, toolRunning: true, toolStart: time.Now().Add(-200 * time.Millisecond)}
	out := b.render(80, false)
	if strings.Contains(out, "✓") {
		t.Fatalf("running tool block should not show success icon: %q", out)
	}
	if !strings.Contains(out, "Running a command") {
		t.Fatalf("running tool block should show friendly label: %q", out)
	}
}

func TestRenderAssistantThinkingCompactVsExpanded(t *testing.T) {
	thinking := "line one\nline two\nline three with a lot of extra detail that only matters when fully expanded"
	b := block{kind: BlockAssistant, raw: "answer", thinking: thinking}
	compact := b.render(80, false)
	expanded := b.render(80, true)
	if compact == expanded {
		t.Fatal("expanded thinking should differ from compact")
	}
	// Compact preview collapses newlines into a single line.
	if strings.Contains(compact, "line one\nline two") {
		t.Fatalf("compact thinking should collapse newlines: %q", compact)
	}
	// Expanded view preserves the full multi-line trace.
	if !strings.Contains(expanded, "line two") || !strings.Contains(expanded, "line three") {
		t.Fatalf("expanded thinking should show full trace: %q", expanded)
	}
}

func TestRenderSystemBlock(t *testing.T) {
	b := block{kind: BlockSystem, raw: "switched mode"}
	if out := b.render(80, false); !strings.Contains(out, "switched mode") {
		t.Fatalf("system block missing text: %q", out)
	}
}
