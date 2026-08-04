package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestStatusLineFillsWidth(t *testing.T) {
	out := statusLine(40, "▌ build · kimi-k2", "? for commands")
	if lipgloss.Width(out) != 40 {
		t.Fatalf("status line width = %d, want 40", lipgloss.Width(out))
	}
}

func TestStatusLineTruncatesOverflow(t *testing.T) {
	left := "▌ verylongmodename-that-overflows-the-bar"
	out := statusLine(20, left, "right")
	if lipgloss.Width(out) > 20 {
		t.Fatalf("status line width = %d, want <= 20", lipgloss.Width(out))
	}
}

// While processing, the spinner/interrupt line sits directly above the
// composer and the idle status (mode + model) stays pinned below it.
func TestStatusLinesSplitAroundComposer(t *testing.T) {
	m, _, _ := newTestModel()
	m.updateSizes()
	m.state = stateProcessing

	view := stripANSI(m.bottomRegion())
	proc := strings.Index(view, "esc to interrupt")
	box := strings.Index(view, "╭") // composer top border
	idle := strings.Index(view, "code──") // idle status: mode label + fill
	if proc < 0 || box < 0 || idle < 0 {
		t.Fatalf("missing segments (proc=%d box=%d idle=%d):\n%s", proc, box, idle, view)
	}
	if proc > box {
		t.Fatalf("processing line should be above the composer:\n%s", view)
	}
	if idle < box {
		t.Fatalf("idle status line should stay below the composer:\n%s", view)
	}
}

// Idle state shows no processing line; the only status bar lives below the
// composer.
func TestStatusLineIdleOnlyBelowComposer(t *testing.T) {
	m, _, _ := newTestModel()
	m.updateSizes()
	m.state = stateInput

	view := stripANSI(m.bottomRegion())
	if strings.Contains(view, "esc to interrupt") {
		t.Fatalf("idle state must not show the processing line:\n%s", view)
	}
	box := strings.Index(view, "╭")
	idle := strings.Index(view, "code──")
	if box < 0 || idle < 0 || idle < box {
		t.Fatalf("idle status line should be below the composer:\n%s", view)
	}
}
