package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/tui/components"
)

func TestPasteStashRoundTrip(t *testing.T) {
	p := newPasteStash()
	big := strings.Repeat("x", 1500)
	placeholder := p.stash(big)
	if !strings.HasPrefix(placeholder, "[Pasted #1") {
		t.Fatalf("placeholder = %q", placeholder)
	}
	expanded, resolved := p.expand("before " + placeholder + " after")
	if !resolved {
		t.Fatal("intact placeholder should resolve")
	}
	if !strings.Contains(expanded, big) {
		t.Fatal("expand did not restore original text")
	}
}

func TestPasteSmallNotStashed(t *testing.T) {
	p := newPasteStash()
	if p.shouldStash("short") {
		t.Fatal("short text should not stash")
	}
}

func TestPasteExpandEditedPlaceholder(t *testing.T) {
	p := newPasteStash()
	big := strings.Repeat("x", 1500)
	placeholder := p.stash(big)
	// User edited the token text (dropped the closing bracket): no token matches.
	edited := placeholder[:len(placeholder)-1]
	expanded, resolved := p.expand("look at " + edited)
	if resolved {
		t.Fatal("edited placeholder should report unresolved")
	}
	if !strings.Contains(expanded, "[Pasted #") {
		t.Fatalf("unresolved text should keep the edited placeholder, got %q", expanded)
	}
}

// Submitting with an edited placeholder warns instead of silently sending
// the model a meaningless "[Pasted #N]" marker.
func TestSubmitWarnsOnEditedPlaceholder(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	m.paste.stash(strings.Repeat("x", 1500))
	m.input.textarea.SetValue("check [Pasted #1 (1500 chars")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	var warned bool
	for _, b := range m.blocks {
		if b.kind == components.BlockSystem && strings.Contains(b.raw, "placeholder") {
			warned = true
		}
	}
	if !warned {
		t.Fatal("expected a system warning about the edited placeholder")
	}
}
