package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/tui/components"
)

// PgUp/PgDn page through the transcript from the input state; Ctrl+Home jumps
// to the oldest message and Ctrl+End returns to the tail.
func TestTranscriptNavigationKeys(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	fillBlocks(m, 60)

	if !m.viewport.AtBottom() {
		t.Fatal("expected to start at bottom")
	}

	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.viewport.AtBottom() {
		t.Fatal("PgUp should leave the bottom")
	}
	if view := m.viewport.View(); !strings.Contains(view, "answer") {
		t.Fatalf("PgUp view shows blank placeholders:\n%q", view)
	}

	m.Update(tea.KeyMsg{Type: tea.KeyCtrlHome})
	if m.viewport.YOffset != 0 {
		t.Fatalf("Ctrl+Home should jump to the top, got YOffset %d", m.viewport.YOffset)
	}

	m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.viewport.YOffset == 0 {
		t.Fatal("PgDown should move away from the top")
	}

	m.Update(tea.KeyMsg{Type: tea.KeyCtrlEnd})
	if !m.viewport.AtBottom() {
		t.Fatal("Ctrl+End should return to the bottom")
	}
}

// The same navigation works while the agent is streaming.
func TestTranscriptNavigationWhileProcessing(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateProcessing
	fillBlocks(m, 60)

	m.Update(tea.KeyMsg{Type: tea.KeyCtrlHome})
	if m.viewport.YOffset != 0 {
		t.Fatalf("Ctrl+Home should jump to the top, got YOffset %d", m.viewport.YOffset)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlEnd})
	if !m.viewport.AtBottom() {
		t.Fatal("Ctrl+End should return to the bottom")
	}
}

// The scrollbar column is blank while the conversation fits the viewport, and
// shows a thumb that moves with the offset once it overflows.
func TestScrollbarIndicator(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	fillBlocks(m, 2)

	bar := m.scrollbarView()
	if strings.Contains(bar, "█") || strings.Contains(bar, "│") {
		t.Fatalf("short transcript should have a blank scrollbar, got %q", bar)
	}

	fillBlocks(m, 60)
	m.viewport.GotoTop()
	bar = m.scrollbarView()
	if !strings.Contains(bar, "█") {
		t.Fatalf("overflowing transcript should show a thumb, got %q", bar)
	}
	lines := strings.Split(bar, "\n")
	if len(lines) != m.viewport.Height {
		t.Fatalf("scrollbar height %d, want %d", len(lines), m.viewport.Height)
	}
	if !strings.Contains(lines[0], "█") {
		t.Fatalf("thumb should sit at the top when scrolled to the top, first line %q", lines[0])
	}

	m.viewport.GotoBottom()
	lines = strings.Split(m.scrollbarView(), "\n")
	if !strings.Contains(lines[len(lines)-1], "█") {
		t.Fatalf("thumb should sit at the bottom when at the tail, last line %q", lines[len(lines)-1])
	}
}

// Ctrl+Y copies the last assistant reply when no block is focused.
func TestCopyLastAssistantReply(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	var copied string
	m.copyFn = func(s string) error { copied = s; return nil }

	m.blocks = append(m.blocks,
		block{kind: components.BlockUser, raw: "question"},
		block{kind: components.BlockAssistant, raw: "first answer"},
		block{kind: components.BlockUser, raw: "follow up"},
		block{kind: components.BlockAssistant, raw: "latest answer"},
	)
	m.components = componentsFromBlocks(m.blocks)

	m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	if copied != "latest answer" {
		t.Fatalf("copied %q, want %q", copied, "latest answer")
	}
	if !strings.Contains(m.notice, "copied") {
		t.Fatalf("expected a copy notice, got %q", m.notice)
	}

	// The notice clears on the next keystroke.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if m.notice != "" {
		t.Fatalf("notice should clear on the next keystroke, got %q", m.notice)
	}
}

// With a block focused, Ctrl+Y copies that block instead of the last reply.
func TestCopyFocusedBlock(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	var copied string
	m.copyFn = func(s string) error { copied = s; return nil }

	m.blocks = append(m.blocks,
		block{kind: components.BlockAssistant, raw: "old answer"},
		block{kind: components.BlockAssistant, raw: "latest answer"},
	)
	m.components = componentsFromBlocks(m.blocks)
	m.focusedBlockIndex = 0

	m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	if copied != "old answer" {
		t.Fatalf("copied %q, want %q", copied, "old answer")
	}
}

// The exit transcript carries user and assistant text in plain form, skips the
// banner, and collapses tool blocks to one-line summaries.
func TestWriteTranscript(t *testing.T) {
	blocks := []block{
		{kind: components.BlockBanner, raw: "\x1b[38mBIG LOGO\x1b[0m"},
		{kind: components.BlockUser, raw: "how do I test?"},
		{kind: components.BlockAssistant, raw: "run go test ./..."},
		{kind: components.BlockTool, toolName: "bash", toolChip: "12 lines"},
		{kind: components.BlockSystem, raw: "\x1b[2minterrupted\x1b[0m"},
	}
	var sb strings.Builder
	writeTranscript(&sb, blocks)
	out := sb.String()

	for _, want := range []string{"> how do I test?", "run go test ./...", "• bash (12 lines)", "interrupted"} {
		if !strings.Contains(out, want) {
			t.Errorf("transcript missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "BIG LOGO") {
		t.Errorf("banner should be skipped:\n%s", out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Errorf("transcript should be plain text without ANSI:\n%q", out)
	}
}

// No conversation, no transcript header polluting the terminal on exit.
func TestWriteTranscriptEmpty(t *testing.T) {
	var sb strings.Builder
	writeTranscript(&sb, []block{{kind: components.BlockBanner, raw: "LOGO"}})
	if sb.String() != "" {
		t.Fatalf("empty conversation should print nothing, got %q", sb.String())
	}
}
