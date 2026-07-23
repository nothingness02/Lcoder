package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const spinnerInterval = 100 * time.Millisecond

var spinnerGlyphs = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

var spinnerPhrases = []string{
	"Thinking", "Working", "Crunching", "Reasoning", "Cooking", "Pondering",
}

// spinnerTickMsg drives spinner animation while processing.
type spinnerTickMsg struct{}

type spinner struct {
	frame int
}

func newSpinner() spinner { return spinner{} }

// advance steps the spinner one frame.
func (s *spinner) advance() { s.frame++ }

func (s spinner) glyph() string {
	return spinnerGlyphs[s.frame%len(spinnerGlyphs)]
}

// phrase rotates every ~50 frames (frames tick ~100ms → ~5s per phrase).
func (s spinner) phrase() string {
	return spinnerPhrases[(s.frame/50)%len(spinnerPhrases)]
}

// view renders the accent-colored glyph plus the shimmering phrase + "…". The
// phrase uses a soft highlight sweep so the working status reads as an animated
// glow (the Kocoro status-line treatment), on the Lcoder cyan gradient.
func (s spinner) view() string {
	return styleAccent().Render(s.glyph()) + " " + renderWaveText(s.phrase()+"…", s.frame)
}

// spinnerTick schedules the next spinner frame. Caller only batches this in
// stateProcessing so it stops when idle.
func spinnerTick() tea.Cmd {
	return tea.Tick(spinnerInterval, func(_ time.Time) tea.Msg { return spinnerTickMsg{} })
}
