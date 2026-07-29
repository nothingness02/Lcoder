package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// superpowersDir locates the cloned obra/superpowers checkout used for
// real-world external skill loading tests. The test skips when absent.
func superpowersDir(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{
		os.Getenv("SUPERPOWERS_DIR"),
		`/tmp/superpowers/skills`,
		filepath.Join(os.TempDir(), "superpowers", "skills"),
	} {
		if candidate == "" {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	t.Skip("superpowers checkout not found; clone https://github.com/obra/superpowers to run")
	return ""
}

// Every skill in a real-world external collection must parse and be
// discoverable — no silent drops.
func TestExternalSuperpowersDiscovery(t *testing.T) {
	dir := superpowersDir(t)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	dirs := 0
	for _, e := range entries {
		if e.IsDir() {
			dirs++
		}
	}

	cat := Discover([]Source{{Scope: ScopeUser, Dir: dir}})
	got := len(cat.Entries())
	if got != dirs {
		t.Fatalf("expected %d skills, discovered %d — some were silently dropped", dirs, got)
	}

	// A known skill parses with its real frontmatter.
	meta, ok := cat.Find("test-driven-development")
	if !ok {
		t.Fatal("test-driven-development should be discovered")
	}
	if meta.Description == "" {
		t.Fatal("description should come from frontmatter")
	}
	t.Logf("discovered %d skills; sample: %s: %s", got, meta.Name, meta.Description)

	// The catalog block renders them.
	block := cat.Block()
	if !strings.Contains(block, "test-driven-development") {
		t.Fatalf("block should list the skill:\n%s", block)
	}
}

// Activation: the full body of a real external skill loads through the same
// path use_skill takes.
func TestExternalSuperpowersActivation(t *testing.T) {
	dir := superpowersDir(t)
	cat := Discover([]Source{{Scope: ScopeUser, Dir: dir}})

	meta, ok := cat.Find("test-driven-development")
	if !ok {
		t.Skip("skill not present in this checkout")
	}
	skill, err := LoadSkill(meta.Source)
	if err != nil {
		t.Fatalf("load skill body: %v", err)
	}
	if !strings.Contains(skill.Body, "test") {
		t.Fatalf("body should contain the skill instructions, got %q", skill.Body[:min(200, len(skill.Body))])
	}
	t.Logf("activated %s: %d bytes of body", meta.Name, len(skill.Body))
}
