package tui

import "testing"

// The shimmer must stay stable: every tick produces a non-empty render with the
// original text intact once ANSI styling is stripped.
func TestRenderWaveTextStable(t *testing.T) {
	text := "Thinking…"
	for tick := 0; tick < 40; tick++ {
		got := renderWaveText(text, tick)
		if got == "" {
			t.Fatalf("tick %d produced empty output", tick)
		}
		if plain := stripANSI(got); plain != text {
			t.Fatalf("tick %d stripped output %q, want %q", tick, plain, text)
		}
	}
}

func TestRenderWaveTextEmpty(t *testing.T) {
	if got := renderWaveText("", 0); got != "" {
		t.Fatalf("empty input should yield empty output, got %q", got)
	}
}

// The highlight period is len(runes)+6; a full period later the render repeats.
func TestRenderWaveTextPeriod(t *testing.T) {
	text := "Working…"
	period := len([]rune(text)) + 6
	for tick := 0; tick < period; tick++ {
		if a, b := renderWaveText(text, tick), renderWaveText(text, tick+period); a != b {
			t.Fatalf("tick %d and %d differ; the n+6 period should repeat", tick, tick+period)
		}
	}
}
