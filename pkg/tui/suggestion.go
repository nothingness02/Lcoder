package tui

import (
	"strings"

	"github.com/lcoder/lcoder/pkg/tui/components"
)
// deriveSuggestion produces a dim follow-up hint, or "" when not applicable.
// It is intentionally cheap and offline: a small heuristic over the last
// assistant message. Swap the body to wire a real completion source later.
func deriveSuggestion(completedTurns int, last *block) string {
	if completedTurns < 1 || last == nil || last.kind != components.BlockAssistant {
		return ""
	}
	text := strings.TrimSpace(last.raw)
	if text == "" {
		return ""
	}
	// If the assistant asked a question, an affirmative is the likely reply.
	if strings.HasSuffix(text, "?") {
		return "yes"
	}
	return ""
}

// updateSuggestion recomputes the ghost text from current model state.
func (m *Model) updateSuggestion() {
	if m.state != stateInput || strings.TrimSpace(m.input.Value()) != "" {
		m.suggestion = ""
		return
	}
	m.suggestion = deriveSuggestion(m.completedTurns, m.lastAssistantBlock())
}

// lastAssistantBlock returns the most recent assistant block, or nil.
func (m *Model) lastAssistantBlock() *block {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == components.BlockAssistant {
			return &m.blocks[i]
		}
	}
	return nil
}

// acceptSuggestion moves the ghost text into the composer.
func (m *Model) acceptSuggestion() {
	if m.suggestion == "" {
		return
	}
	m.input.textarea.SetValue(m.suggestion)
	m.suggestion = ""
}
