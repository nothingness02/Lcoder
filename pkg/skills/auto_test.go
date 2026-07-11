package skills

import (
	"testing"
)

func TestAutoDetect(t *testing.T) {
	catalog := []SkillMeta{
		{
			Name:        "security-review",
			Description: "Review code for security vulnerabilities",
			Keywords:    []string{"security", "review"},
		},
		{
			Name:        "refactor",
			Description: "Refactor code following project conventions",
			Keywords:    []string{"refactor", "improve"},
		},
	}

	score, ok := AutoDetect("Please review auth.go for security issues", catalog)
	if !ok {
		t.Fatal("expected a skill match")
	}
	if score.Skill.Name != "security-review" {
		t.Fatalf("expected security-review, got %s", score.Skill.Name)
	}
}

func TestAutoDetectNoMatch(t *testing.T) {
	catalog := []SkillMeta{
		{
			Name:        "security-review",
			Description: "Review code for security vulnerabilities",
		},
	}

	_, ok := AutoDetect("What is the weather today?", catalog)
	if ok {
		t.Fatal("expected no skill match")
	}
}
