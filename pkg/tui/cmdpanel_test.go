package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/mcp"
	"github.com/lcoder/lcoder/pkg/skills"
)

func TestCmdPanelHelpShowsPanelNotBlock(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	m.dispatchSlash("/help")

	if !m.cmdPanel.visible {
		t.Fatal("expected cmd panel visible for /help")
	}
	if m.cmdPanel.kind != cmdPanelText {
		t.Fatalf("expected text panel, got %v", m.cmdPanel.kind)
	}
	for _, b := range m.blocks {
		if b.kind == BlockSystem {
			t.Fatalf("expected no system block, got %q", b.raw)
		}
	}
}

func TestCmdPanelModesSelectsAndSwitches(t *testing.T) {
	dir, err := os.MkdirTemp("", "lcoder-modes-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	content := `name: review
description: Review mode
system_prompt: you review
`
	if err := os.WriteFile(filepath.Join(dir, "review.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	mm := agent.NewModeManager()
	if err := mm.LoadModes([]string{dir}); err != nil {
		t.Fatal(err)
	}

	bus := events.New()
	ag := &fakeAgent{}
	sess := &fakeSession{ID: "abc123"}
	m := NewModel(bus, ag, sess, &fakeSessionStore{}, ".", "abc123", "openai/gpt-4o-mini", "dark", nil, nil, mm, nil, config.Config{}, nil, false)
	m.width = 80
	m.height = 24
	m.state = stateInput

	m.dispatchSlash("/modes")
	if !m.cmdPanel.visible || m.cmdPanel.kind != cmdPanelSelect {
		t.Fatalf("expected select panel, got %v", m.cmdPanel)
	}

	var reviewIdx int
	found := false
	for i, it := range m.cmdPanel.items {
		if it.value == "review" {
			reviewIdx = i
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("review mode not in items: %v", m.cmdPanel.items)
	}

	// Move selection to the review item.
	for m.cmdPanel.selected < reviewIdx {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(*Model)
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := updated.(*Model)
	if m2.cmdPanel.visible {
		t.Fatal("expected panel closed after Enter")
	}
	if cmd != nil {
		t.Fatal("expected no command from mode switch")
	}
	if m2.agent.Mode() != "review" {
		t.Fatalf("expected mode review, got %s", m2.agent.Mode())
	}
}

func TestCmdPanelEscClosesTextPanel(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	m.dispatchSlash("/status")
	if !m.cmdPanel.visible {
		t.Fatal("panel should be visible")
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m2 := updated.(*Model)
	if m2.cmdPanel.visible {
		t.Fatal("expected panel closed by Esc")
	}
}

func TestCmdPanelTypingDismisses(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	m.dispatchSlash("/help")
	if !m.cmdPanel.visible {
		t.Fatal("panel should be visible")
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m2 := updated.(*Model)
	if m2.cmdPanel.visible {
		t.Fatal("expected panel closed on typing")
	}
}

func TestCmdPanelSkillTriggers(t *testing.T) {
	bus := events.New()
	ag := &fakeAgent{}
	sess := &fakeSession{ID: "abc123"}

	dir := t.TempDir()
	source := filepath.Join(dir, "SKILL.md")
	content := `---
name: security-review
description: Review code
---
Review the code for risks.
`
	if err := os.WriteFile(source, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded := []skills.SkillMeta{
		{Name: "security-review", Description: "Review code", Source: source},
	}
	m := NewModel(bus, ag, sess, &fakeSessionStore{}, ".", "abc123", "openai/gpt-4o-mini", "dark", nil, nil, nil, nil, config.Config{}, nil, false, loaded...)
	m.width = 80
	m.height = 24
	m.state = stateInput

	m.dispatchSlash("/skill")
	if !m.cmdPanel.visible || m.cmdPanel.kind != cmdPanelSelect {
		t.Fatalf("expected skill select panel, got %v", m.cmdPanel)
	}
	if len(m.cmdPanel.items) != 1 || m.cmdPanel.items[0].value != "security-review" {
		t.Fatalf("unexpected items: %v", m.cmdPanel.items)
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := updated.(*Model)
	if m2.cmdPanel.visible {
		t.Fatal("expected panel closed after selecting skill")
	}
	if cmd == nil {
		t.Fatal("expected prompt command after skill trigger")
	}
}

func TestCmdPanelMCPOpensAndReconnectShortcut(t *testing.T) {
	reg := mcp.NewRegistry([]mcp.ServerConfig{
		{Name: "remote", Transport: "sse", URL: "http://127.0.0.1:0", Timeout: 1},
	})
	bus := events.New()
	ag := &fakeAgent{}
	sess := &fakeSession{ID: "abc123"}
	m := NewModel(bus, ag, sess, &fakeSessionStore{}, ".", "abc123", "openai/gpt-4o-mini", "dark", nil, reg, nil, nil, config.Config{}, nil, false)
	m.width = 80
	m.height = 24
	m.state = stateInput

	m.dispatchSlash("/mcp")
	if !m.cmdPanel.visible || m.cmdPanel.kind != cmdPanelSelect {
		t.Fatalf("expected mcp select panel, got %v", m.cmdPanel)
	}
	if m.cmdPanel.action != actionMCPManage {
		t.Fatalf("expected actionMCPManage, got %v", m.cmdPanel.action)
	}
	if len(m.cmdPanel.items) != 1 || m.cmdPanel.items[0].value != "remote" {
		t.Fatalf("unexpected items: %v", m.cmdPanel.items)
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m2 := updated.(*Model)
	if m2.cmdPanel.visible {
		t.Fatal("expected panel closed after r")
	}
	if cmd == nil {
		t.Fatal("expected reconnect command")
	}
	msg := cmd()
	action, ok := msg.(mcpActionMsg)
	if !ok {
		t.Fatalf("expected mcpActionMsg, got %T", msg)
	}
	if action.name != "remote" || action.op != "reconnect" {
		t.Fatalf("unexpected action: %+v", action)
	}
}

func TestCmdPanelMCPCloseShortcut(t *testing.T) {
	reg := mcp.NewRegistry([]mcp.ServerConfig{
		{Name: "remote", Transport: "sse", URL: "http://127.0.0.1:0", Timeout: 1},
	})
	bus := events.New()
	ag := &fakeAgent{}
	sess := &fakeSession{ID: "abc123"}
	m := NewModel(bus, ag, sess, &fakeSessionStore{}, ".", "abc123", "openai/gpt-4o-mini", "dark", nil, reg, nil, nil, config.Config{}, nil, false)
	m.width = 80
	m.height = 24
	m.state = stateInput

	m.dispatchSlash("/mcp")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m2 := updated.(*Model)
	if m2.cmdPanel.visible {
		t.Fatal("expected panel closed after c")
	}
	if cmd == nil {
		t.Fatal("expected close command")
	}
	msg := cmd()
	action, ok := msg.(mcpActionMsg)
	if !ok {
		t.Fatalf("expected mcpActionMsg, got %T", msg)
	}
	if action.name != "remote" || action.op != "close" {
		t.Fatalf("unexpected action: %+v", action)
	}
}
