package memory

import (
	"strings"
	"testing"
)

func TestParseEntriesSkipsHeaderAndSplitsOnSeparator(t *testing.T) {
	input := `═══════════════════════════════════════
MEMORY [10% — 220/2,200 chars]
═══════════════════════════════════════
Project uses Go 1.25.
§
Always run go vet before push.
`
	entries := parseEntries(input)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", len(entries), entries)
	}
	if !strings.Contains(entries[0], "Go 1.25") {
		t.Fatalf("unexpected first entry: %q", entries[0])
	}
	if !strings.Contains(entries[1], "go vet") {
		t.Fatalf("unexpected second entry: %q", entries[1])
	}
}

func TestParseEntriesEmpty(t *testing.T) {
	if parseEntries("") != nil {
		t.Fatal("expected nil for empty input")
	}
	if parseEntries("   \n\n  ") != nil {
		t.Fatal("expected nil for whitespace-only input")
	}
}

func TestFindEntryIndexUniqueSubstring(t *testing.T) {
	entries := []string{"foo bar", "baz qux"}
	idx, err := findEntryIndex(entries, "baz")
	if err != nil || idx != 1 {
		t.Fatalf("expected idx=1, got %d err=%v", idx, err)
	}
}

func TestFindEntryIndexNoMatch(t *testing.T) {
	_, err := findEntryIndex([]string{"a", "b"}, "z")
	if err == nil {
		t.Fatal("expected error for no match")
	}
}

func TestFindEntryIndexAmbiguous(t *testing.T) {
	_, err := findEntryIndex([]string{"abc", "abcd"}, "ab")
	if err == nil {
		t.Fatal("expected error for ambiguous match")
	}
}

func TestCharCount(t *testing.T) {
	if got := charCount([]string{"abc", "de"}); got != 5 {
		t.Fatalf("expected 5, got %d", got)
	}
	if got := charCount(nil); got != 0 {
		t.Fatalf("expected 0, got %d", got)
	}
}

func TestFormatFile(t *testing.T) {
	out := formatFile("MEMORY", []string{"one", "two"}, 100)
	if !strings.Contains(out, "MEMORY") {
		t.Fatal("expected title in output")
	}
	if !strings.Contains(out, "§") {
		t.Fatal("expected separator in output")
	}
}
