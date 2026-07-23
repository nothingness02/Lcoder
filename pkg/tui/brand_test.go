package tui

import (
	"strings"
	"testing"
)

func TestSwirlGridLineCount(t *testing.T) {
	for frame := 0; frame <= headerTotalFrames; frame++ {
		if lines := renderSwirlGrid(frame); len(lines) != swirlSize/2 {
			t.Fatalf("frame %d has %d lines, want %d", frame, len(lines), swirlSize/2)
		}
	}
}

func TestSwirlGridColumnsBounded(t *testing.T) {
	for frame := 0; frame <= headerTotalFrames; frame++ {
		for _, ln := range renderSwirlGrid(frame) {
			if n := len([]rune(stripANSI(ln))); n != swirlSize {
				t.Fatalf("frame %d line has %d runes, want %d", frame, n, swirlSize)
			}
		}
	}
}

func TestSwirlGridRevealsTopToBottom(t *testing.T) {
	count := func(frame int) int {
		n := 0
		for _, ln := range renderSwirlGrid(frame) {
			if strings.TrimSpace(stripANSI(ln)) != "" {
				n++
			}
		}
		return n
	}
	if count(0) >= count(headerTotalFrames - 1) {
		t.Fatalf("expected more revealed rows later: frame0=%d final=%d", count(0), count(headerTotalFrames-1))
	}
}
