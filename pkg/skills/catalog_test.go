package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestSkill(t *testing.T, base, name, desc string) {
	t.Helper()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + desc + "\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Higher-priority scopes override same-name skills; groups render in
// project-first order.
func TestDiscoverPriorityOverride(t *testing.T) {
	user := t.TempDir()
	project := t.TempDir()
	writeTestSkill(t, user, "helper", "from user")
	writeTestSkill(t, project, "helper", "from project")
	writeTestSkill(t, user, "user-only", "u")

	cat := Discover([]Source{
		{Scope: ScopeUser, Dir: user},
		{Scope: ScopeProject, Dir: project},
	})

	meta, ok := cat.Find("helper")
	if !ok || meta.Description != "from project" {
		t.Fatalf("project should override user for same-name skill, got %+v", meta)
	}
	if meta.Scope != ScopeProject {
		t.Fatalf("winner scope = %v, want project", meta.Scope)
	}

	block := cat.Block()
	if !strings.Contains(block, "from project") || !strings.Contains(block, "from user") == false {
		t.Fatalf("block should render the project winner:\n%s", block)
	}
	if !strings.Contains(block, "### Project") || !strings.Contains(block, "### User") {
		t.Fatalf("block should group by scope:\n%s", block)
	}
	if !strings.Contains(block, "Project overrides User overrides Built-in") {
		t.Fatal("block should state the override rule")
	}
}

// Hidden and disabled skills are excluded from the model-facing block but
// stay in the entry list.
func TestBlockExcludesHiddenAndDisabled(t *testing.T) {
	dir := t.TempDir()
	writeTestSkill(t, dir, "visible", "v")
	writeTestSkill(t, dir, "shy", "s")
	// mark shy as hidden
	data, _ := os.ReadFile(filepath.Join(dir, "shy", "SKILL.md"))
	data = []byte(strings.Replace(string(data), "description: s", "description: s\nhidden: true", 1))
	_ = os.WriteFile(filepath.Join(dir, "shy", "SKILL.md"), data, 0o644)

	cat := Discover([]Source{{Scope: ScopeUser, Dir: dir}})
	cat.SetDisabled("visible", true)

	block := cat.Block()
	if strings.Contains(block, "shy") || strings.Contains(block, "- visible") {
		t.Fatalf("hidden/disabled skills must not appear in the block:\n%s", block)
	}
	if len(cat.Entries()) != 2 {
		t.Fatalf("entries keep all skills, got %d", len(cat.Entries()))
	}
}

// The catalog budget folds overflow into a "+N more" line.
func TestBlockBudgetFoldsOverflow(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 60; i++ {
		writeTestSkill(t, dir, strings.Repeat("x", 8)+string(rune('a'+i%26))+strings.Repeat("y", 60), strings.Repeat("d", 100))
	}
	cat := Discover([]Source{{Scope: ScopeUser, Dir: dir}})
	block := cat.Block()
	if len(block) > catalogBudget+1024 { // groups + fold line slack
		t.Fatalf("block exceeds budget: %d chars", len(block))
	}
	if !strings.Contains(block, "more skills") {
		t.Fatalf("overflow should be folded:\n%s...", block[:200])
	}
}

// Disabled state persists to and reloads from the YAML file.
func TestDisabledFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skills.yaml")
	if err := SaveDisabledFile(path, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadDisabledFile(path)
	if err != nil || len(got) != 2 {
		t.Fatalf("round trip: %v %v", got, err)
	}
	if _, err := LoadDisabledFile(filepath.Join(t.TempDir(), "missing.yaml")); err != nil {
		t.Fatalf("missing file should be nil, got %v", err)
	}
}

// Sub-skill nesting: has-sub-skill packages recurse; children are namespaced
// parent.child, hidden from the catalog block, but activatable by name.
func TestSubSkillNesting(t *testing.T) {
	base := t.TempDir()
	// parent without has-sub-skill: child dir must NOT be discovered.
	writeTestSkill(t, base, "plain", "p")
	// parent with has-sub-skill.
	parent := filepath.Join(base, "pdf")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(parent, "SKILL.md"), []byte("---\nname: pdf\ndescription: PDF tools\nhas-sub-skill: true\n---\nparent body\n"), 0o644)
	// child skill inside the parent package.
	writeTestSkill(t, parent, "extract-table", "extract tables")
	// a stray dir under plain/ (no has-sub-skill) must be ignored.
	writeTestSkill(t, filepath.Join(base, "plain"), "inner", "ignored")

	cat := Discover([]Source{{Scope: ScopeUser, Dir: base}})

	child, ok := cat.Find("pdf.extract-table")
	if !ok {
		t.Fatalf("child skill not discovered: %v", cat.Entries())
	}
	if !child.IsSubSkill {
		t.Fatal("child must be marked IsSubSkill")
	}
	if _, ok := cat.Find("plain.inner"); ok {
		t.Fatal("child of a non-has-sub-skill parent must not be discovered")
	}

	block := cat.Block()
	if strings.Contains(block, "extract-table") {
		t.Fatalf("sub-skills must not appear in the catalog block:\n%s", block)
	}
	if !strings.Contains(block, "pdf") {
		t.Fatalf("parent should be listed:\n%s", block)
	}
}
