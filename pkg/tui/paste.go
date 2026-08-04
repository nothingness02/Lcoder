package tui

import (
	"fmt"
	"strings"
	"time"
)

// pasteThreshold is the rune count above which a paste is folded to a
// placeholder. Workload: pasting a large file/log into the composer; symptom
// when it binds: the composer balloons and the layout jumps. Override: bump this
// const.
const pasteThreshold = 1000

// pasteBurstWindow is the inter-key interval (inclusive) below which a key
// belongs to a fast input burst (terminal paste or macro replay). Human
// typing is far slower (>80ms/keystroke), so 50ms cleanly separates bursts
// from manual input.
const pasteBurstWindow = 50 * time.Millisecond

// burstMinChars is the minimum number of consecutive fast keystrokes before
// an Enter may count as a literal newline. It prevents a lone quick Enter
// (fast manual submit) or a short "ok"+Enter from being misclassified.
const burstMinChars = 3

// burstEnter reports whether this Enter key belongs to a fast input burst
// (terminal paste or macro replay) and should be inserted as a newline
// instead of submitting. It also resets the burst counter.
//
// Unix/ANSI terminals merge pastes into a single bracketed-paste KeyMsg, but
// Windows coninput delivers each pasted character as an independent key event
// with no marker — a \r in the paste arrives as an ordinary KeyEnter. Without
// this guard every newline in a Windows paste would submit a separate message.
func (m *Model) burstEnter() bool {
	now := time.Now()
	burst := !m.lastKeyAt.IsZero() && now.Sub(m.lastKeyAt) < pasteBurstWindow && m.burstChars >= burstMinChars
	m.lastKeyAt = now
	m.burstChars = 0
	return burst
}

// noteKey advances the burst tracker for a regular composing keystroke. Fast
// consecutive keys accumulate burstChars; a slow key (or long pause) restarts
// the run so a manual "type then enter" never trips burstEnter.
func (m *Model) noteKey() {
	now := time.Now()
	if !m.lastKeyAt.IsZero() && now.Sub(m.lastKeyAt) < pasteBurstWindow {
		m.burstChars++
	} else {
		m.burstChars = 1
	}
	m.lastKeyAt = now
}

type pasteStash struct {
	items map[int]string
	next  int
}

func newPasteStash() *pasteStash {
	return &pasteStash{items: map[int]string{}, next: 1}
}

func (p *pasteStash) shouldStash(s string) bool {
	return len([]rune(s)) > pasteThreshold
}

// stash stores s and returns a placeholder token to insert in the composer.
func (p *pasteStash) stash(s string) string {
	id := p.next
	p.next++
	p.items[id] = s
	return fmt.Sprintf("[Pasted #%d (%d chars)]", id, len([]rune(s)))
}

// expand replaces every placeholder token in text with its stashed content.
// It reports false when an edited placeholder remains unresolved — the text
// then goes out as-is with a warning rather than silently sending the model
// a meaningless "[Pasted #N]" marker.
func (p *pasteStash) expand(text string) (string, bool) {
	for id, content := range p.items {
		token := fmt.Sprintf("[Pasted #%d (%d chars)]", id, len([]rune(content)))
		text = strings.ReplaceAll(text, token, content)
	}
	return text, !strings.Contains(text, "[Pasted #")
}
