package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/tui/components"
)

func TestPasteStashRoundTrip(t *testing.T) {
	p := newPasteStash()
	big := strings.Repeat("x", 1500)
	placeholder := p.stash(big)
	if !strings.HasPrefix(placeholder, "[Pasted #1") {
		t.Fatalf("placeholder = %q", placeholder)
	}
	expanded, resolved := p.expand("before " + placeholder + " after")
	if !resolved {
		t.Fatal("intact placeholder should resolve")
	}
	if !strings.Contains(expanded, big) {
		t.Fatal("expand did not restore original text")
	}
}

func TestPasteSmallNotStashed(t *testing.T) {
	p := newPasteStash()
	if p.shouldStash("short") {
		t.Fatal("short text should not stash")
	}
}

func TestPasteExpandEditedPlaceholder(t *testing.T) {
	p := newPasteStash()
	big := strings.Repeat("x", 1500)
	placeholder := p.stash(big)
	// User edited the token text (dropped the closing bracket): no token matches.
	edited := placeholder[:len(placeholder)-1]
	expanded, resolved := p.expand("look at " + edited)
	if resolved {
		t.Fatal("edited placeholder should report unresolved")
	}
	if !strings.Contains(expanded, "[Pasted #") {
		t.Fatalf("unresolved text should keep the edited placeholder, got %q", expanded)
	}
}

// Submitting with an edited placeholder warns instead of silently sending
// the model a meaningless "[Pasted #N]" marker.
func TestSubmitWarnsOnEditedPlaceholder(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	m.paste.stash(strings.Repeat("x", 1500))
	m.input.textarea.SetValue("check [Pasted #1 (1500 chars")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	var warned bool
	for _, b := range m.blocks {
		if b.kind == components.BlockSystem && strings.Contains(b.raw, "placeholder") {
			warned = true
		}
	}
	if !warned {
		t.Fatal("expected a system warning about the edited placeholder")
	}
}

// ── Paste-burst Enter detection (Windows coninput) ──────────────────────────

// burstAt rewinds m's burst tracker to a state equivalent to "lastKeyAt at
// offset before now" and the given run length.
func burstAt(m *Model, before time.Duration, chars int) {
	m.lastKeyAt = time.Now().Add(-before)
	m.burstChars = chars
}

// An Enter inside a fast multi-char burst is a literal newline (pasted \r on
// Windows), never a submit.
func TestBurstEnterInFastMultiCharBurst(t *testing.T) {
	m, _, _ := newTestModel()
	burstAt(m, 10*time.Millisecond, 3)
	if !m.burstEnter() {
		t.Fatal("Enter inside a fast multi-char burst should be treated as a newline")
	}
	// burstEnter resets the counter for the next key.
	if m.burstChars != 0 {
		t.Fatalf("burst counter should reset after burstEnter, got %d", m.burstChars)
	}
}

// A slow Enter (human typing) is a submit, regardless of run length.
func TestBurstEnterSlowKeyIsSubmit(t *testing.T) {
	m, _, _ := newTestModel()
	burstAt(m, 200*time.Millisecond, 20)
	if m.burstEnter() {
		t.Fatal("slow Enter (long gap) must stay a submit")
	}
}

// A fast Enter with too few preceding characters (e.g. quick "ok"+Enter) is
// still a submit — only a real burst of >= burstMinChars keys qualifies.
func TestBurstEnterShortBurstStillSubmits(t *testing.T) {
	m, _, _ := newTestModel()
	burstAt(m, 10*time.Millisecond, 2)
	if m.burstEnter() {
		t.Fatal("short burst below burstMinChars must stay a submit")
	}
}

// noteKey accumulates fast consecutive keys into a run and restarts it on a
// slow key, so manual typing never accidentally reaches burst status.
func TestNoteKeyAccumulatesAndResets(t *testing.T) {
	m, _, _ := newTestModel()
	m.noteKey() // first key: run = 1
	m.noteKey()
	m.noteKey()
	if m.burstChars != 3 {
		t.Fatalf("fast consecutive keys should accumulate to 3, got %d", m.burstChars)
	}
	// A slow key restarts the run.
	m.lastKeyAt = time.Now().Add(-pasteBurstWindow - 10*time.Millisecond)
	m.noteKey()
	if m.burstChars != 1 {
		t.Fatalf("slow key should restart the run at 1, got %d", m.burstChars)
	}
}
