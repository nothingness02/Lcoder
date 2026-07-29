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

// Deny rules are absolute: a more specific allow from a later (higher
// precedence) source cannot punch through a generic deny. To relax a deny,
// the later source must override the SAME pattern.
func TestDenyIsAbsoluteAcrossSources(t *testing.T) {
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

	if got := engine.Evaluate(Request{Tool: "bash", Command: "go test ./..."}); got != Deny {
		t.Fatalf("deny rules must win over more specific allows, got %v", got)
	}
}

// At the same pattern, a later source overrides an earlier one — this is how
// a project relaxes a deny inherited from the global config.
func TestSamePatternLaterSourceWins(t *testing.T) {
	engine := NewEngine(Config{
		Rules: map[string]RuleTable{
			"bash": {"go *": Deny},
		},
	})
	engine.AddSource("project", Config{
		Rules: map[string]RuleTable{
			"bash": {"go *": Allow},
		},
	})

	if got := engine.Evaluate(Request{Tool: "bash", Command: "go test ./..."}); got != Allow {
		t.Fatalf("project source should override the same pattern, got %v", got)
	}
}

func TestUnsafeModeAllowsAllExceptUltraDestructive(t *testing.T) {
	engine := NewEngine(DefaultConfig())
	engine.SetUnsafeMode(true)

	if got := engine.Evaluate(Request{Tool: "bash", Command: "curl http://example.com"}); got != Allow {
		t.Fatalf("expected unsafe allow, got %v", got)
	}
	if got := engine.Evaluate(Request{Tool: "bash", Command: "rm -rf /"}); got != Ask {
		t.Fatalf("expected ultra-destructive ask, got %v", got)
	}
	if got := engine.Evaluate(Request{Tool: "write", Path: "/etc/passwd"}); got != Allow {
		t.Fatalf("expected unsafe allow for write, got %v", got)
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

// A forked engine shares rule sources and session approvals with its parent
// but owns its guard policies — the property in-process subagents rely on.
func TestForkSharesRulesButNotGuards(t *testing.T) {
	parent := NewEngine(Config{
		Rules: map[string]RuleTable{"bash": {"*": Ask}},
	})
	child := parent.Fork()

	// Session approvals flow parent -> child and child -> parent.
	parent.AddSessionRule("bash", map[string]any{"command": "make build"})
	if got := child.Evaluate(Request{Tool: "bash", Command: "make build"}); got != Allow {
		t.Fatalf("child should see the parent's session approval, got %v", got)
	}
	child.AddSessionRule("bash", map[string]any{"command": "make test"})
	if got := parent.Evaluate(Request{Tool: "bash", Command: "make test"}); got != Allow {
		t.Fatalf("parent should see the child's session approval, got %v", got)
	}

	// Rules learned by the child (a new source) are visible to the parent.
	child.AddSource("project", Config{
		Rules: map[string]RuleTable{"write": {"*": Allow}},
	})
	if got := parent.Evaluate(Request{Tool: "write", Path: "a.go"}); got != Allow {
		t.Fatalf("parent should see sources added via the child, got %v", got)
	}

	// Guards are per-instance: the child's deny guard does not affect the parent.
	child.SetGuardPolicies(staticPolicy{name: "mode-guard", tool: "bash", decision: Deny})
	if got := child.Evaluate(Request{Tool: "bash", Command: "ls"}); got != Deny {
		t.Fatalf("child guard should deny, got %v", got)
	}
	if got := parent.Evaluate(Request{Tool: "bash", Command: "ls"}); got == Deny {
		t.Fatal("child guard must not leak into the parent engine")
	}
}

func TestForkInheritsPathContextAndUnsafeMode(t *testing.T) {
	dir := t.TempDir()
	parent := NewEngine(DefaultConfig())
	parent.SetPathContext(dir, "")
	parent.SetUnsafeMode(true)

	child := parent.Fork()
	if !child.UnsafeMode() {
		t.Fatal("fork should inherit unsafe mode")
	}
	child.AddSessionRule("write", map[string]any{"path": "a.txt"})
	if got := child.Evaluate(Request{Tool: "write", Path: filepath.Join(dir, "a.txt")}); got != Allow {
		t.Fatalf("fork should inherit path context (canonical session target), got %v", got)
	}
}
