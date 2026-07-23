package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/tui/components"
)

// TestColorPanelOpensAndAppliesAccent verifies /color opens a select panel of
// accent presets and choosing a row swaps the global accent token.
func TestColorPanelOpensAndAppliesAccent(t *testing.T) {
	origAccent := colorAccent
	origComponents := components.ColorAccent
	defer func() {
		colorAccent = origAccent
		components.ColorAccent = origComponents
	}()

	bus := events.New()
	ag := &fakeAgent{}
	sess := &fakeSession{ID: "abc123"}
	m := NewModel(bus, ag, sess, &fakeSessionStore{}, ".", "abc123", "openai/gpt-4o-mini", "dark", nil, nil, nil, nil, config.Config{}, nil, false)
	m.width = 80
	m.height = 24
	m.state = stateInput

	m.dispatchSlash("/color")
	if !m.cmdPanel.visible || m.cmdPanel.kind != cmdPanelSelect {
		t.Fatalf("expected color select panel, got %v", m.cmdPanel)
	}
	if m.cmdPanel.action != actionApplyAccent {
		t.Fatalf("expected actionApplyAccent, got %v", m.cmdPanel.action)
	}
	if len(m.cmdPanel.items) != len(accentPresets) {
		t.Fatalf("expected %d presets, got %d", len(accentPresets), len(m.cmdPanel.items))
	}

	// Move down to the second preset (kocoro) and apply it.
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.cmdPanel.visible {
		t.Fatal("expected panel closed after applying accent")
	}
	if colorAccent != components.ColorAccent {
		t.Fatalf("local and canonical accent diverged after apply")
	}
	want := accentPresets[1]
	if colorAccent.Light != want.light || colorAccent.Dark != want.dark {
		t.Fatalf("accent not swapped to %s: got light=%s dark=%s, want light=%s dark=%s",
			want.name, colorAccent.Light, colorAccent.Dark, want.light, want.dark)
	}
}
