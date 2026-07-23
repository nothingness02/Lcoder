package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestMenuExactPrefixFirst(t *testing.T) {
	matches := menuMatches("he")
	if len(matches) == 0 {
		t.Fatal("no matches for 'he'")
	}
	if matches[0].entry.Name != "help" {
		t.Fatalf("first match = %q, want help", matches[0].entry.Name)
	}
}

func TestMenuFuzzy(t *testing.T) {
	matches := menuMatches("sesn")
	found := false
	for _, m := range matches {
		if m.entry.Name == "sessions" {
			found = true
		}
	}
	if !found {
		t.Fatal("fuzzy did not match 'sessions' for 'sesn'")
	}
}

func TestMenuRenderHighlights(t *testing.T) {
	matches := menuMatches("hel")
	out := renderMenu(matches, 0, 40)
	if !strings.Contains(stripANSI(out), "help") {
		t.Fatal("menu render missing help")
	}
}

func TestMenuEmptyQueryListsAll(t *testing.T) {
	if len(menuMatches("")) != len(commandRegistry) {
		t.Fatal("empty query should list all commands")
	}
}

func TestMenuFuzzyRequiresThreeChars(t *testing.T) {
	// "io" is a subsequence of "sessions" but below the 3-char fuzzy threshold
	// and not a prefix of any command, so it should match nothing.
	if matches := menuMatches("io"); len(matches) != 0 {
		t.Fatalf("want no matches below the fuzzy threshold, got %d", len(matches))
	}
	// "sesn" (≥3 chars) reaches the fuzzy path and matches "sessions".
	if matches := menuMatches("sesn"); len(matches) == 0 {
		t.Fatal("want fuzzy matches at/above the threshold")
	}
}

func TestMenuRenderFixedHeight(t *testing.T) {
	// One match vs. all matches render the same number of lines: the window is
	// padded to a fixed row count so the box height doesn't jump while typing.
	few := renderMenu(menuMatches("hel"), 0, 40)
	many := renderMenu(menuMatches(""), 0, 40)
	if lipgloss.Height(few) != lipgloss.Height(many) {
		t.Fatalf("menu height not fixed: few=%d many=%d", lipgloss.Height(few), lipgloss.Height(many))
	}
}
