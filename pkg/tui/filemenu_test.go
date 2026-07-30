package tui

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestActiveMentionAt(t *testing.T) {
	cases := []struct {
		name    string
		val     string
		cursor  int // absolute rune offset, clamped into [0, len]
		partial string
		at      int
		end     int
		ok      bool
	}{
		{"trailing partial", "see @ma", 7, "ma", 4, 7, true},
		{"bare at", "@", 1, "", 0, 1, true},
		{"at without leading space", "foo@bar", 7, "", 0, 0, false},
		{"closed by space", "@done file", 10, "", 0, 0, false},
		{"cursor right after word", "@done file", 5, "done", 0, 5, true},
		{"no mention", "no mention", 10, "", 0, 0, false},
		{"last of two mentions", "a @b c @d", 9, "d", 7, 9, true},
		{"first of two mentions", "a @b c @d", 4, "b", 2, 4, true},
		{"cursor mid token", "@word", 3, "wo", 0, 5, true},
		{"empty token after space", "a @b ", 5, "", 0, 0, false},
		{"cursor at zero", "@a", 0, "", 0, 0, false},
		{"multiline mention", "line1\n@xy", 9, "xy", 6, 9, true},
		{"multiline cursor on first line", "line1\n@xy", 2, "", 0, 0, false},
		{"cjk before mention", "改 @a 和 @bc", 10, "bc", 7, 10, true},
		{"cjk first mention", "改 @a 和 @bc", 4, "a", 2, 4, true},
		{"cursor clamped past end", "@ab", 99, "ab", 0, 3, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			partial, at, end, ok := activeMentionAt(c.val, c.cursor)
			if ok != c.ok || partial != c.partial || at != c.at || end != c.end {
				t.Errorf("activeMentionAt(%q, %d) = %q,%d,%d,%v want %q,%d,%d,%v",
					c.val, c.cursor, partial, at, end, ok, c.partial, c.at, c.end, c.ok)
			}
		})
	}
}

func TestFileMatches(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "main.go"), "x")
	mustWrite(t, filepath.Join(dir, "pkg", "loop.go"), "x")
	mustWrite(t, filepath.Join(dir, ".git", "config"), "x")
	mustWrite(t, filepath.Join(dir, "node_modules", "dep.js"), "x")

	all := fileMatches(dir, "")
	for _, f := range all {
		if f == ".git/config" || f == "node_modules/dep.js" {
			t.Fatalf("fileMatches included skipped dir entry: %q (all=%v)", f, all)
		}
	}

	got := fileMatches(dir, "loop")
	if !reflect.DeepEqual(got, []string{"pkg/loop.go"}) {
		t.Fatalf("fileMatches(loop) = %v", got)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
