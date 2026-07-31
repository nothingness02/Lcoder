package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/skills"
	"github.com/lcoder/lcoder/pkg/tui/components"
)

// newSkillModel builds a model with one loaded skill for trigger tests.
func newSkillModel(t *testing.T) (*Model, *fakeAgent, *fakeSession) {
	bus := events.New()
	agent := &fakeAgent{}
	sess := &fakeSession{ID: "abc123"}
	store := &fakeSessionStore{}

	dir := t.TempDir()
	source := filepath.Join(dir, "SKILL.md")
	content := `---
name: tester
description: writing tests
---
Write a test for the user's request.
`
	if err := os.WriteFile(source, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	skill := skills.SkillMeta{Name: "tester", Description: "writing tests", Source: source}
	catalog := skills.NewCatalog([]skills.ScopedMeta{{SkillMeta: skill}})
	m := NewModel(bus, agent, sess, store, ".", "abc123", "openai/gpt-4o-mini", "dark", nil, nil, nil, config.Config{}, nil, false, catalog)
	m.width = 80
	m.height = 24
	m.state = stateInput
	return m, agent, sess
}

func TestSubmitManualSkillTrigger(t *testing.T) {
	m, _, _ := newSkillModel(t)

	cmd := m.submit("/skill:tester add a case")
	if cmd == nil {
		t.Fatal("expected a command from a manual skill trigger")
	}
	if m.state != stateProcessing {
		t.Fatalf("expected stateProcessing, got %v", m.state)
	}
	// The skill body is folded into the user message (queued for session
	// append by the runner), not written as a separate system message.
	var activated, folded bool
	for _, b := range m.blocks {
		if b.kind == components.BlockSystem && strings.Contains(b.raw, "activated skill: tester") {
			activated = true
		}
		if b.kind == components.BlockUser &&
			strings.Contains(b.raw, "You are now using the tester skill") &&
			strings.Contains(b.raw, "add a case") {
			folded = true
		}
	}
	if !activated {
		t.Fatal("expected an 'activated skill' system block")
	}
	if !folded {
		t.Fatal("expected the user block to contain the expanded skill body and request")
	}
}

func TestSubmitUnknownSkillTrigger(t *testing.T) {
	m, _, _ := newSkillModel(t)

	cmd := m.submit("/skill:nope do it")
	if cmd != nil {
		t.Fatal("expected no command for an unknown skill")
	}
	if m.state != stateInput {
		t.Fatalf("expected to stay in stateInput, got %v", m.state)
	}
}
