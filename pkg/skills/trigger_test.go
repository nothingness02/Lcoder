package skills

import (
	"strings"
	"testing"
)

func TestParseManualTrigger(t *testing.T) {
	name, rest, ok := ParseManualTrigger("/skill:security-review check auth.go")
	if !ok {
		t.Fatal("expected trigger")
	}
	if name != "security-review" {
		t.Fatalf("expected security-review, got %s", name)
	}
	if rest != "check auth.go" {
		t.Fatalf("expected 'check auth.go', got %s", rest)
	}
}

func TestParseManualTriggerNoSkill(t *testing.T) {
	_, _, ok := ParseManualTrigger("hello world")
	if ok {
		t.Fatal("expected no trigger")
	}
}

func TestExpandManualTrigger(t *testing.T) {
	msg := ExpandManualTrigger(Skill{
		SkillMeta: SkillMeta{
			Name:        "security-review",
			Description: "Review code for vulnerabilities",
		},
		Body: "Read the file and identify risks.",
	}, "check auth.go")
	if msg.Role != "user" {
		t.Fatalf("expected user message, got %s", msg.Role)
	}
	text := msg.Text()
	if !strings.Contains(text, "Read the file and identify risks.") {
		t.Fatalf("expected skill body in message, got %q", text)
	}
	if !strings.Contains(text, "check auth.go") {
		t.Fatalf("expected user request in message, got %q", text)
	}
}
