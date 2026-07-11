package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/skills"
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
	m := NewModel(bus, agent, sess, store, ".", "abc123", "openai/gpt-4o-mini", "dark", nil, nil, nil, nil, config.Config{}, nil, false, skill)
	m.width = 80
	m.height = 24
	m.state = stateInput
	return m, agent, sess
}

func TestSubmitManualSkillTrigger(t *testing.T) {
	m, _, sess := newSkillModel(t)

	cmd := m.submit("/skill:tester add a case")
	if cmd == nil {
		t.Fatal("expected a command from a manual skill trigger")
	}
	if m.state != stateProcessing {
		t.Fatalf("expected stateProcessing, got %v", m.state)
	}
	// ExpandManualTrigger appends a system + user message to the session.
	if len(sess.Messages) != 2 {
		t.Fatalf("expected 2 expanded messages appended, got %d", len(sess.Messages))
	}
	var activated bool
	for _, b := range m.blocks {
		if b.kind == blockSystem && strings.Contains(b.raw, "activated skill: tester") {
			activated = true
		}
	}
	if !activated {
		t.Fatal("expected an 'activated skill' system block")
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
