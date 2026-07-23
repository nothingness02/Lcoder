package tui

import (
	"strings"
	"unicode/utf8"

	"github.com/lcoder/lcoder/pkg/tui/components"
)
// deriveSuggestion produces a dim follow-up hint, or "" when not applicable.
// It is intentionally cheap and offline: a small heuristic over the last
// assistant message. (A real LLM-fork completion source was considered and
// rejected — it would cost an extra token-bearing call per turn; see the plan.)
func deriveSuggestion(completedTurns int, last *block) string {
	if completedTurns < 1 || last == nil || last.kind != components.BlockAssistant {
		return ""
	}
	text := strings.TrimSpace(last.raw)
	if text == "" {
		return ""
	}
	// A trailing question invites a reply. Match both the ASCII '?' and the
	// fullwidth '?' used in CJK text. When the assistant laid out numbered
	// options, "1" (pick the first) is the most likely answer; otherwise a
	// plain affirmative is.
	// fullwidthQuestion is the CJK fullwidth question mark U+FF1F, written as a
	// hex constant so source encodings can't fold it into ASCII '?'.
	const fullwidthQuestion = rune(0xFF1F)
	lastRune, _ := utf8.DecodeLastRuneInString(text)
	switch lastRune {
	case '?', fullwidthQuestion:
		if countNumberedOptions(text) >= 2 {
			return "1"
		}
		return "yes"
	}
	return ""
}

// countNumberedOptions counts lines that open a numbered list item ("1." "2)").
func countNumberedOptions(text string) int {
	n := 0
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(ln)
		if len(ln) >= 2 && ln[0] >= '1' && ln[0] <= '9' && (ln[1] == '.' || ln[1] == ')') {
			n++
		}
	}
	return n
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
