package tui

import (
	"strings"
	"testing"
)

func TestBoundStreamTailShortUnchanged(t *testing.T) {
	if got := boundStreamTail("short", streamLiveMaxBytes); got != "short" {
		t.Fatalf("short input changed: got %q want short", got)
	}
}

func TestBoundStreamTailClipsLongToSuffixAtLineBoundary(t *testing.T) {
	const cap = 16
	// 3-char repeating line "xY\n" repeated 100 times = 300 bytes, well over cap.
	long := strings.Repeat("xY\n", 100)
	got := boundStreamTail(long, cap)
	if len(got) > cap {
		t.Fatalf("tail len %d exceeds cap %d", len(got), cap)
	}
	if !strings.HasSuffix(long, got) {
		t.Fatalf("tail %q is not a suffix of input", got)
	}
	// Tail must start at a line boundary: the byte preceding it in the original
	// (if any) is a newline.
	if got != "" {
		idx := len(long) - len(got) - 1
		if idx >= 0 && long[idx] != '\n' {
			t.Fatalf("tail does not start at line boundary: preceding byte %q", long[idx])
		}
	}
}
