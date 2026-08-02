package tui

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestMentionChipsTrackInput(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	dir := t.TempDir()
	m.cwd = dir
	mustWrite(t, filepath.Join(dir, "main.go"), "x")
	mustWrite(t, filepath.Join(dir, "config.yml"), "x")

	// Resolved mentions appear; unresolved ones are silently omitted.
	m.input.textarea.SetValue("review @main.go and @nope.go and @config.yml")
	m.refreshMenu()
	want := []string{"main.go", "config.yml"}
	if !reflect.DeepEqual(m.mentionChips, want) {
		t.Fatalf("mentionChips = %v, want %v", m.mentionChips, want)
	}

	// No mentions → no chips row.
	m.input.textarea.SetValue("plain text")
	m.refreshMenu()
	if m.mentionChips != nil {
		t.Fatalf("mentionChips = %v, want nil", m.mentionChips)
	}
}

func TestMentionChipsRenderInBottomRegion(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	m.width = 80
	m.height = 24
	dir := t.TempDir()
	m.cwd = dir
	mustWrite(t, filepath.Join(dir, "main.go"), "x")
	m.updateSizes()

	m.input.textarea.SetValue("review @main.go")
	m.refreshMenu()
	out := m.bottomRegion()
	if !strings.Contains(out, "main.go") {
		t.Fatalf("bottom region should show the chips row, got %q", out)
	}
}

func TestMentionChipsClearOnSubmit(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	dir := t.TempDir()
	m.cwd = dir
	mustWrite(t, filepath.Join(dir, "main.go"), "x")
	// Deterministic file menu: no matches, so Enter submits instead of being
	// consumed by the @-file menu. The real suggester's backend differs by
	// platform (fd on CI, FileIndex locally) and by timing (sync fd vs async
	// index scan), which would flip fileMenuVisible and break this test.
	m.fileSuggester = &stubSuggester{ready: true}

	m.input.textarea.SetValue("review @main.go")
	m.refreshMenu()
	if len(m.mentionChips) == 0 {
		t.Fatal("expected chips before submit")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.mentionChips != nil {
		t.Fatalf("chips should clear on submit, got %v", m.mentionChips)
	}
}

// The frame must stay within the terminal height when the composer (or any
// bottom-region row) grows outside a resize.
func TestFrameStaysWithinTerminalOnComposerGrow(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	m.width = 80
	m.height = 24
	m.updateSizes()

	for range 150 {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	}
	frame := m.View()
	if lines := strings.Count(frame, "\n") + 1; lines > m.height {
		t.Fatalf("frame overflows terminal: %d lines > %d", lines, m.height)
	}
}
