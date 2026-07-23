package tui

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/tui/components"
)
func TestDeriveSuggestion_Gating(t *testing.T) {
	// No completed turns: no suggestion.
	if s := deriveSuggestion(0, nil); s != "" {
		t.Fatalf("want empty before any turn, got %q", s)
	}
}

func TestDeriveSuggestion_QuestionPromptsAffirmative(t *testing.T) {
	last := &block{kind: components.BlockAssistant, raw: "Do you want me to run the tests?"}
	s := deriveSuggestion(1, last)
	if s == "" {
		t.Fatalf("want a suggestion after a question, got empty")
	}
}

func TestDeriveSuggestion_NumberedOptionsPickFirst(t *testing.T) {
	last := &block{kind: components.BlockAssistant, raw: "Pick one:\n1. run the tests\n2. skip them\nWhich?"}
	if s := deriveSuggestion(1, last); s != "1" {
		t.Fatalf("want \"1\" for numbered options, got %q", s)
	}
}

func TestDeriveSuggestion_CJKQuestion(t *testing.T) {
	last := &block{kind: components.BlockAssistant, raw: "要我继续吗?"}
	if s := deriveSuggestion(1, last); s != "yes" {
		t.Fatalf("want \"yes\" for a CJK question, got %q", s)
	}
}

func TestDeriveSuggestion_SingleOptionStaysAffirmative(t *testing.T) {
	// Only one numbered line (<2 options) is not an option list.
	last := &block{kind: components.BlockAssistant, raw: "See 1. the note above. Continue?"}
	if s := deriveSuggestion(1, last); s != "yes" {
		t.Fatalf("want \"yes\" when fewer than two options, got %q", s)
	}
}

func TestSuggestionAccept(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	m.suggestion = "run the tests"
	m.acceptSuggestion()
	if m.input.Value() != "run the tests" {
		t.Fatalf("want composer filled with suggestion, got %q", m.input.Value())
	}
	if m.suggestion != "" {
		t.Fatalf("want suggestion cleared after accept")
	}
}
