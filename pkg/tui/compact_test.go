package tui

import (
	"strings"
	"testing"
)

func TestFormatTokenCount(t *testing.T) {
	cases := map[int]string{
		0:       "0",
		999:     "999",
		1000:    "1,000",
		12345:   "12,345",
		1234567: "1,234,567",
	}
	for in, want := range cases {
		if got := formatTokenCount(in); got != want {
			t.Fatalf("formatTokenCount(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatCompactResult(t *testing.T) {
	out := stripANSI(formatCompactResult(12345, 1200, "folded the early turns"))
	if !strings.Contains(out, "12,345") || !strings.Contains(out, "1,200") {
		t.Fatalf("want before/after token counts, got %q", out)
	}
	if !strings.Contains(out, "folded the early turns") {
		t.Fatalf("want summary echoed, got %q", out)
	}
}

func TestFormatCompactResultTruncatesSummary(t *testing.T) {
	long := strings.Repeat("a", 300)
	out := stripANSI(formatCompactResult(100, 50, long))
	if !strings.Contains(out, strings.Repeat("a", compactSummaryMaxRunes)+"…") {
		t.Fatalf("want summary truncated at %d runes, got %q", compactSummaryMaxRunes, out)
	}
	if strings.Contains(out, strings.Repeat("a", compactSummaryMaxRunes+1)) {
		t.Fatalf("summary should be truncated, got %q", out)
	}
}

func TestFormatCompactResultNoSummary(t *testing.T) {
	out := stripANSI(formatCompactResult(100, 50, ""))
	if strings.Contains(out, "\n") {
		t.Fatalf("empty summary should not add a second line, got %q", out)
	}
}
