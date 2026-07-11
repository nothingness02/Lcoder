package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSkill(t *testing.T) {
	dir, err := os.MkdirTemp("", "lcoder-skills-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	skillDir := filepath.Join(dir, "security-review")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `---
name: security-review
description: Review code for security vulnerabilities
keywords:
  - security
  - review
---
# Security Review

Do a security review.
`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	catalog, err := LoadCatalog([]string{dir})
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	if len(catalog) != 1 {
		t.Fatalf("expected 1 skill meta, got %d", len(catalog))
	}
	s, err := LoadSkill(catalog[0].Source)
	if err != nil {
		t.Fatalf("load skill: %v", err)
	}
	if s.Name != "security-review" {
		t.Fatalf("expected security-review, got %s", s.Name)
	}
	if s.Description != "Review code for security vulnerabilities" {
		t.Fatalf("unexpected description: %s", s.Description)
	}
	if !contains(s.Keywords, "security") {
		t.Fatalf("expected security keyword, got %v", s.Keywords)
	}
	if s.Body == "" {
		t.Fatal("expected non-empty body")
	}
}

func TestLoadCatalogSkipsBody(t *testing.T) {
	dir, err := os.MkdirTemp("", "lcoder-skills-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	skillDir := filepath.Join(dir, "security-review")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `---
name: security-review
description: Review code for security vulnerabilities
---
# Security Review

Do a security review.
`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	catalog, err := LoadCatalog([]string{dir})
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	if len(catalog) != 1 {
		t.Fatalf("expected 1 skill meta, got %d", len(catalog))
	}
	meta := catalog[0]
	if meta.Description != "Review code for security vulnerabilities" {
		t.Fatalf("unexpected description: %s", meta.Description)
	}
	if meta.Source == "" {
		t.Fatal("expected source path")
	}
}

func contains(items []string, target string) bool {
	for _, it := range items {
		if it == target {
			return true
		}
	}
	return false
}
