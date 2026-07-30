package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// topBarMinWidth hides the bar below this many columns; /status carries the
// same information for very narrow terminals.
const topBarMinWidth = 50

// topBarInfo is the identity strip rendered above the transcript.
type topBarInfo struct {
	mode    string
	model   string
	session string
	turn    int
}

// renderTopBar draws the persistent one-line header:
//
//	Lcoder · plan · claude-sonnet-4-5 · session a1b2c3d4 · turn 12
//
// Segments are dropped from the right when the width runs out (mode is always
// kept); below topBarMinWidth the bar disappears entirely.
func renderTopBar(info topBarInfo, width int) string {
	if width < topBarMinWidth {
		return ""
	}
	mode := info.mode
	if mode == "" {
		mode = "ready"
	}
	session := info.session
	if len(session) > 8 {
		session = session[:8]
	}
	dim := styleDim()
	segments := []string{
		styleAccent().Bold(true).Render("Lcoder"),
		dim.Render(mode),
		dim.Render(info.model),
	}
	if session != "" {
		segments = append(segments, dim.Render("session "+session))
	}
	if info.turn > 0 {
		segments = append(segments, dim.Render(fmt.Sprintf("turn %d", info.turn)))
	}

	sep := dim.Render(" · ")
	// Drop trailing segments until the bar fits.
	for len(segments) > 2 {
		line := " " + strings.Join(segments, sep)
		if lipgloss.Width(line) <= width {
			return line
		}
		segments = segments[:len(segments)-1]
	}
	return " " + strings.Join(segments, sep)
}

// topBarView renders the bar for the current model state, or "" when hidden.
func (m *Model) topBarView() string {
	if !m.topBar {
		return ""
	}
	session := ""
	if m.session != nil {
		session = m.session.SessionID()
	}
	return renderTopBar(topBarInfo{
		mode:    m.modeLabel(),
		model:   m.model,
		session: session,
		turn:    m.completedTurns,
	}, m.width)
}

// topBarHeight returns the rows the bar occupies (0 when hidden).
func (m *Model) topBarHeight() int {
	if m.topBarView() == "" {
		return 0
	}
	return 1
}
