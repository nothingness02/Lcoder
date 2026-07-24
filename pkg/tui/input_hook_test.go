package tui

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/tui/components"
)

// newHookModel builds a minimal model for submit-path input hook tests.
func newHookModel(t *testing.T) *Model {
	t.Helper()
	m := NewModel(events.New(), &fakeAgent{}, &fakeSession{ID: "abc123"}, &fakeSessionStore{}, ".", "abc123", "openai/gpt-4o-mini", "dark", nil, nil, nil, nil, config.Config{}, nil, false)
	m.width = 80
	m.height = 24
	m.state = stateInput
	return m
}

func TestSubmitInputHookTransform(t *testing.T) {
	m := newHookModel(t)
	m.SetInputHook(func(text string) (string, bool, string) {
		return text + " [hooked]", true, ""
	})
	m.submit("hi")
	found := false
	for _, b := range m.blocks {
		if b.kind == components.BlockUser && b.raw == "hi [hooked]" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected transformed user block, got %+v", m.blocks)
	}
}

func TestSubmitInputHookBlock(t *testing.T) {
	m := newHookModel(t)
	m.SetInputHook(func(text string) (string, bool, string) {
		return text, false, "blocked by ext"
	})
	m.submit("bad")
	if len(m.blocks) != 1 {
		t.Fatalf("expected a single block notice, got %+v", m.blocks)
	}
	if m.blocks[0].kind != components.BlockSystem || m.blocks[0].raw == "bad" {
		t.Fatalf("expected a system block notice, got %+v", m.blocks[0])
	}
	if !strings.Contains(m.blocks[0].raw, "blocked by ext") {
		t.Fatalf("expected notice to contain the block reason, got %+v", m.blocks[0])
	}
	if m.state != stateInput {
		t.Fatalf("expected to stay in stateInput, got %v", m.state)
	}
}

func TestSubmitInputHookSkipsSlashCommands(t *testing.T) {
	m := newHookModel(t)
	called := false
	m.SetInputHook(func(text string) (string, bool, string) {
		called = true
		return text, true, ""
	})
	m.submit("/help")
	if called {
		t.Fatal("input hook must not run for slash commands")
	}
}
