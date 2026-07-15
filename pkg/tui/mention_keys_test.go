package tui

import (
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// A missing @file mention blocks submit: input is kept and an error panel shows.
func TestMissingMentionBlocksSubmit(t *testing.T) {
	m, agent, _ := newTestModel()
	m.state = stateInput
	m.cwd = t.TempDir()

	m.input.textarea.SetValue("look at @nope.go")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := updated.(*Model)

	if m2.state != stateInput {
		t.Fatalf("expected to stay in stateInput, got %v", m2.state)
	}
	if m2.input.Value() != "look at @nope.go" {
		t.Fatalf("expected input preserved, got %q", m2.input.Value())
	}
	if !m2.cmdPanel.visible {
		t.Fatal("expected error panel to be visible")
	}
	if len(agent.Prompts) != 0 {
		t.Fatalf("expected no prompt dispatched, got %d", len(agent.Prompts))
	}
	for _, b := range m2.blocks {
		if b.kind == BlockUser {
			t.Fatal("expected no user block recorded")
		}
	}
}

// A valid @file mention submits and records the file basename as an attachment.
func TestValidMentionSubmitsWithAttachment(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	dir := t.TempDir()
	m.cwd = dir
	mustWrite(t, filepath.Join(dir, "main.go"), "x")

	m.input.textarea.SetValue("review @main.go")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := updated.(*Model)

	if cmd == nil {
		t.Fatal("expected a command after submit")
	}
	if m2.state != stateProcessing {
		t.Fatalf("expected stateProcessing, got %v", m2.state)
	}
	var found *block
	for i := range m2.blocks {
		if m2.blocks[i].kind == BlockUser {
			found = &m2.blocks[i]
		}
	}
	if found == nil {
		t.Fatal("expected a user block")
	}
	if len(found.attachments) != 1 || found.attachments[0] != "main.go" {
		t.Fatalf("expected attachment [main.go], got %v", found.attachments)
	}
}
