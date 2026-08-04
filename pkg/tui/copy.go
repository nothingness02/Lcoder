package tui

import (
	"fmt"

	"github.com/lcoder/lcoder/pkg/tui/components"
)

// copyTargetText returns the raw text Ctrl+Y should copy: the focused block
// when one is focused, else the most recent assistant message. Tool blocks
// copy their full result content (block.raw), matching the expanded view.
func (m *Model) copyTargetText() string {
	if m.focusedBlockIndex >= 0 && m.focusedBlockIndex < len(m.blocks) {
		return m.blocks[m.focusedBlockIndex].raw
	}
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == components.BlockAssistant {
			return m.blocks[i].raw
		}
	}
	return ""
}

// copyBlock copies the target text to the clipboard and surfaces the outcome
// as a transient notice above the composer.
func (m *Model) copyBlock() {
	text := m.copyTargetText()
	if text == "" {
		m.notice = "nothing to copy yet"
		return
	}
	fn := m.copyFn
	if fn == nil {
		fn = copyTextToClipboard
	}
	if err := fn(text); err != nil {
		m.notice = "clipboard copy failed: " + err.Error()
		return
	}
	m.notice = fmt.Sprintf("copied %d chars to clipboard", len(text))
}
