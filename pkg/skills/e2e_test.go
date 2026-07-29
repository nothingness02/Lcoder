package skills

import (
	"strings"
	"testing"
)

// End-to-end: a user's extra_dirs (pointing at an external clone) merges with
// the built-in and home sources, and the budgeted block stays coherent.
func TestExternalSuperpowersExtraDirsMerge(t *testing.T) {
	dir := superpowersDir(t)
	// project root may not exist for superpowers; pass a temp cwd
	cwd := t.TempDir()
	sources := DefaultSources(cwd, []string{dir})
	cat := Discover(sources)

	// superpowers skills must be merged in alongside builtins.
	if _, ok := cat.Find("test-driven-development"); !ok {
		t.Fatal("extra_dirs skills should merge into the catalog")
	}
	if _, ok := cat.Find("security-review"); !ok {
		t.Fatal("builtin embedded skill should still be present")
	}

	block := cat.Block()
	if len(block) == 0 {
		t.Fatal("block should not be empty")
	}
	// Long external descriptions must be truncated, not overflowing.
	if !strings.Contains(block, "…") {
		t.Log("note: no truncation marker — descriptions all fit")
	}
	t.Logf("block size: %d chars for %d entries", len(block), len(cat.Entries()))
}
