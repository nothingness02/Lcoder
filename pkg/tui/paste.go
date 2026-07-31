package tui

import (
	"fmt"
	"strings"
)

// pasteThreshold is the rune count above which a paste is folded to a
// placeholder. Workload: pasting a large file/log into the composer; symptom
// when it binds: the composer balloons and the layout jumps. Override: bump this
// const.
const pasteThreshold = 1000

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
