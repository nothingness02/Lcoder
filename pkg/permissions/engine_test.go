package permissions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProjectRules(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "permissions.yaml")
	content := `permissions:
  rules:
    bash:
      "go test *": allow
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	engine := NewEngine(DefaultConfig())
	if err := engine.LoadProjectRules(path); err != nil {
		t.Fatal(err)
	}
	if engine.Evaluate(Request{Tool: "bash", Command: "go test ./..."}) != Allow {
		t.Fatal("expected project rule to allow go test")
	}
}

func TestProjectRuleOverridesGlobal(t *testing.T) {
	engine := NewEngine(Config{
		Rules: map[string]RuleTable{
			"bash": {"go *": Deny},
		},
	})
	engine.AddSource("project", Config{
		Rules: map[string]RuleTable{
			"bash": {"go test *": Allow},
		},
	})

	if got := engine.Evaluate(Request{Tool: "bash", Command: "go test ./..."}); got != Allow {
		t.Fatalf("expected project-specific allow, got %v", got)
	}
}

func TestDefaultAllow(t *testing.T) {
	engine := NewEngine(DefaultConfig())
	if engine.Evaluate(Request{Tool: "read"}) != Allow {
		t.Fatal("expected allow by default")
	}
}

func TestSpecificity(t *testing.T) {
	cfg := Config{
		Rules: map[string]RuleTable{
			"bash": {
				"*":     Ask,
				"git *": Allow,
			},
		},
	}
	engine := NewEngine(cfg)

	if engine.Evaluate(Request{Tool: "bash", Command: "rm -rf /"}) != Ask {
		t.Fatal("expected ask for generic bash")
	}
	if engine.Evaluate(Request{Tool: "bash", Command: "git status"}) != Allow {
		t.Fatal("expected allow for git command")
	}
}

func TestBashClassification(t *testing.T) {
	cfg := Config{
		Rules: map[string]RuleTable{
			"bash": {
				"*":           Ask,
				"ls *":        Allow,
				"git status":  Allow,
				"git status *": Allow,
				"go test *":   Allow,
				"rm -rf /":    Deny,
				"rm -rf /*":   Deny,
				"sudo *":      Deny,
				"mkfs.*":      Deny,
			},
		},
	}
	engine := NewEngine(cfg)

	cases := []struct {
		cmd  string
		want Decision
	}{
		{"ls -la", Allow},
		{"git status", Allow},
		{"git status --short", Allow},
		{"go test ./...", Allow},
		{"unknown command", Ask},
		{"rm -rf /", Deny},
		{"rm -rf /tmp", Deny},
		{"sudo apt update", Deny},
		{"mkfs.ext4 /dev/sda", Deny},
	}
	for _, c := range cases {
		got := engine.Evaluate(Request{Tool: "bash", Command: c.cmd})
		if got != c.want {
			t.Errorf("%q: got %v, want %v", c.cmd, got, c.want)
		}
	}
}

func TestDeny(t *testing.T) {
	cfg := Config{
		Rules: map[string]RuleTable{
			"read": {
				"*.env": Deny,
				"*":     Allow,
			},
		},
	}
	engine := NewEngine(cfg)

	if engine.Evaluate(Request{Tool: "read", Path: ".env"}) != Deny {
		t.Fatal("expected deny for .env")
	}
	if engine.Evaluate(Request{Tool: "read", Path: "main.go"}) != Allow {
		t.Fatal("expected allow for main.go")
	}
}
