package tui

import "testing"

func TestDeriveSuggestion_Gating(t *testing.T) {
	// No completed turns: no suggestion.
	if s := deriveSuggestion(0, nil); s != "" {
		t.Fatalf("want empty before any turn, got %q", s)
	}
}

func TestDeriveSuggestion_QuestionPromptsAffirmative(t *testing.T) {
	last := &block{kind: BlockAssistant, raw: "Do you want me to run the tests?"}
	s := deriveSuggestion(1, last)
	if s == "" {
		t.Fatalf("want a suggestion after a question, got empty")
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
