package builtin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/skills"
)

// writeTestSkill lays down a SKILL.md under dir/<name> and returns its catalog entry.
func writeTestSkill(t *testing.T, dir, name, frontmatter, body string) skills.SkillMeta {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(skillDir, "SKILL.md")
	content := "---\nname: " + name + "\n" + frontmatter + "---\n" + body
	if err := os.WriteFile(source, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog, err := skills.LoadCatalog([]string{dir})
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	meta, ok := skills.FindByName(catalog, name)
	if !ok {
		t.Fatalf("skill %q not in catalog", name)
	}
	return meta
}

func TestUseSkillDefinition(t *testing.T) {
	tool := NewUseSkill(".", skills.NewCatalog(nil))
	def := tool.Definition()
	if def.Name != skills.UseSkillToolName {
		t.Fatalf("expected name %q, got %q", skills.UseSkillToolName, def.Name)
	}
}

func TestUseSkillExecuteReturnsBody(t *testing.T) {
	dir := t.TempDir()
	meta := writeTestSkill(t, dir, "security-review",
		"description: Review code for vulnerabilities\n",
		"# Security Review\n\nRead the file and identify risks.\n")

	tool := NewUseSkill(".", skills.NewCatalog([]skills.ScopedMeta{{SkillMeta: meta}}))
	result, err := tool.Execute(context.Background(), "call_1", map[string]any{"skill_name": "security-review"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(result.Text(), "Read the file and identify risks.") {
		t.Fatalf("expected skill body in result, got %q", result.Text())
	}
	// Unrestricted skills still publish the key (empty) so the executor lifts
	// any previous restriction.
	v, ok := result.Details[skills.AllowedToolsDetailsKey]
	if !ok {
		t.Fatal("expected allowed-tools details key on success")
	}
	if names, _ := v.([]string); len(names) != 0 {
		t.Fatalf("expected empty allowed-tools list, got %v", names)
	}
}

func TestUseSkillExecutePublishesAllowedTools(t *testing.T) {
	dir := t.TempDir()
	meta := writeTestSkill(t, dir, "security-review",
		"description: Review code for vulnerabilities\nallowed_tools:\n  - read\n  - grep\n",
		"# Security Review\n\nRead the file and identify risks.\n")

	tool := NewUseSkill(".", skills.NewCatalog([]skills.ScopedMeta{{SkillMeta: meta}}))
	result, err := tool.Execute(context.Background(), "call_1", map[string]any{"skill_name": "security-review"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	names, _ := result.Details[skills.AllowedToolsDetailsKey].([]string)
	if len(names) != 2 || names[0] != "read" || names[1] != "grep" {
		t.Fatalf("expected [read grep], got %v", names)
	}
	if !strings.Contains(result.Text(), "read, grep") {
		t.Fatalf("expected restriction hint in result text, got %q", result.Text())
	}
}

func TestUseSkillExecuteUnknownSkill(t *testing.T) {
	dir := t.TempDir()
	meta := writeTestSkill(t, dir, "security-review", "description: x\n", "body\n")

	tool := NewUseSkill(".", skills.NewCatalog([]skills.ScopedMeta{{SkillMeta: meta}}))
	_, err := tool.Execute(context.Background(), "call_1", map[string]any{"skill_name": "nope"})
	if err == nil {
		t.Fatal("expected error for unknown skill")
	}
	if !strings.Contains(err.Error(), "security-review") {
		t.Fatalf("expected available names in error, got %q", err.Error())
	}
}

func TestUseSkillExecuteMissingName(t *testing.T) {
	tool := NewUseSkill(".", skills.NewCatalog(nil))
	if _, err := tool.Execute(context.Background(), "call_1", map[string]any{}); err == nil {
		t.Fatal("expected error for missing skill_name")
	}
}

// A disabled skill is rejected with an actionable message.
func TestUseSkillRejectsDisabled(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "SKILL.md")
	_ = os.WriteFile(source, []byte("---\nname: x\ndescription: d\n---\nbody\n"), 0o644)
	cat := skills.NewCatalog([]skills.ScopedMeta{{SkillMeta: skills.SkillMeta{Name: "x", Description: "d", Source: source}}})
	cat.SetDisabled("x", true)

	tool := NewUseSkill(".", cat)
	_, err := tool.Execute(context.Background(), "c1", map[string]any{"skill_name": "x"})
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected disabled rejection, got %v", err)
	}
}
