package skills

import (
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
	msgs := ExpandManualTrigger(Skill{
		SkillMeta: SkillMeta{
			Name:        "security-review",
			Description: "Review code for vulnerabilities",
		},
		Body: "Read the file and identify risks.",
	}, "/skill:security-review check auth.go")
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "system" {
		t.Fatalf("expected system message, got %s", msgs[0].Role)
	}
	if msgs[1].Role != "user" {
		t.Fatalf("expected user message, got %s", msgs[1].Role)
	}
}
