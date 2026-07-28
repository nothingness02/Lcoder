package permissions

import (
	"strings"
	"testing"
)

// staticPolicy is a test guard policy with a fixed opinion for one tool.
type staticPolicy struct {
	name     string
	tool     string
	decision Decision
	reason   string
}

func (p staticPolicy) Name() string { return p.name }

func (p staticPolicy) Decide(req Request) (Decision, string, bool) {
	if req.Tool != p.tool {
		return "", "", false
	}
	return p.decision, p.reason, true
}

func TestGuardPoliciesRunBeforeEverything(t *testing.T) {
	engine := NewEngine(Config{
		Rules: map[string]RuleTable{
			"write": {"*": Allow},
		},
	})
	engine.SetGuardPolicies(staticPolicy{name: "mode-guard", tool: "write", decision: Deny, reason: "no writes in plan mode"})

	decision, policy, reason := engine.EvaluateWithSource(Request{Tool: "write", Path: "main.go"})
	if decision != Deny || policy != "mode-guard" {
		t.Fatalf("guard should decide first, got %v by %s", decision, policy)
	}
	if !strings.Contains(reason, "plan mode") {
		t.Fatalf("guard reason should reach the caller, got %q", reason)
	}

	// Guards also hold under unsafe mode.
	engine.SetUnsafeMode(true)
	if got := engine.Evaluate(Request{Tool: "write", Path: "main.go"}); got != Deny {
		t.Fatalf("guard deny must hold in unsafe mode, got %v", got)
	}
}

func TestGuardWithoutOpinionFallsThrough(t *testing.T) {
	engine := NewEngine(Config{
		Rules: map[string]RuleTable{
			"write": {"*": Allow},
		},
	})
	engine.SetGuardPolicies(staticPolicy{name: "mode-guard", tool: "bash", decision: Deny})

	decision, policy, _ := engine.EvaluateWithSource(Request{Tool: "write", Path: "main.go"})
	if decision != Allow || policy != "user-rule" {
		t.Fatalf("expected user-rule allow, got %v by %s", decision, policy)
	}
}

func TestEvaluateWithSourceNamesPolicies(t *testing.T) {
	engine := NewEngine(Config{
		Rules: map[string]RuleTable{
			"bash": {"rm *": Deny, "ls *": Allow, "*": Ask},
		},
	})

	cases := []struct {
		req    Request
		want   Decision
		policy string
	}{
		{Request{Tool: "bash", Command: "rm -rf /tmp"}, Deny, "user-deny-rule"},
		{Request{Tool: "bash", Command: "ls -la"}, Allow, "user-rule"},
		{Request{Tool: "bash", Command: "make"}, Ask, "user-rule"},
		{Request{Tool: "write", Path: "x.go"}, Deny, "dangerous-default"},
		{Request{Tool: "read", Path: "x.go"}, Allow, "default-allow"},
	}
	for _, c := range cases {
		got, policy, _ := engine.EvaluateWithSource(c.req)
		if got != c.want || policy != c.policy {
			t.Errorf("%+v: got %v by %s, want %v by %s", c.req, got, policy, c.want, c.policy)
		}
	}
}

// Between matching ask and allow rules, the longest (most specific) pattern
// wins; deny rules are checked before either regardless of specificity.
func TestChainPrioritySemantics(t *testing.T) {
	engine := NewEngine(Config{
		Rules: map[string]RuleTable{
			"bash": {
				"*":           Ask,
				"git *":       Allow,
				"git push *":  Ask,
				"git commit ": Allow,
			},
		},
	})

	if got := engine.Evaluate(Request{Tool: "bash", Command: "git status"}); got != Allow {
		t.Fatalf("more specific allow should beat generic ask, got %v", got)
	}
	if got := engine.Evaluate(Request{Tool: "bash", Command: "git push origin"}); got != Ask {
		t.Fatalf("more specific ask should beat generic allow, got %v", got)
	}

	engine.AddSource("project", Config{Rules: map[string]RuleTable{"bash": {"git push --force *": Deny}}})
	if got := engine.Evaluate(Request{Tool: "bash", Command: "git push --force origin"}); got != Deny {
		t.Fatalf("deny must win regardless of specificity, got %v", got)
	}
}

func TestExplainNamesPolicyAndReason(t *testing.T) {
	engine := NewEngine(Config{
		Rules: map[string]RuleTable{"bash": {"rm *": Deny}},
	})
	got := engine.Explain(Request{Tool: "bash", Command: "rm -rf /tmp"})
	if !strings.Contains(got, "user-deny-rule") || !strings.Contains(got, `rm *`) {
		t.Fatalf("explanation should name policy and rule, got %q", got)
	}
}

func TestSessionApprovalBeatsStaticAsk(t *testing.T) {
	engine := NewEngine(Config{
		Rules: map[string]RuleTable{
			"bash": {"*": Ask},
		},
	})

	// Before approval: ask.
	if got := engine.Evaluate(Request{Tool: "bash", Command: "make build"}); got != Ask {
		t.Fatalf("expected ask before approval, got %v", got)
	}

	// Approve this exact command for the session.
	engine.AddSessionRule("bash", map[string]any{"command": "make build"})

	decision, policy, _ := engine.EvaluateWithSource(Request{Tool: "bash", Command: "make build"})
	if decision != Allow || policy != "session-approval" {
		t.Fatalf("session approval should beat the static ask rule, got %v by %s", decision, policy)
	}

	// Exact match only: a different command still asks.
	if got := engine.Evaluate(Request{Tool: "bash", Command: "make test"}); got != Ask {
		t.Fatalf("session approval must not generalize, got %v", got)
	}

	// Clearing resets.
	engine.ClearSessionRules()
	if got := engine.Evaluate(Request{Tool: "bash", Command: "make build"}); got != Ask {
		t.Fatalf("cleared session approval should ask again, got %v", got)
	}
}

func TestSessionApprovalNeverBeatsDeny(t *testing.T) {
	engine := NewEngine(Config{
		Rules: map[string]RuleTable{
			"bash": {"rm *": Deny},
		},
	})
	engine.AddSessionRule("bash", map[string]any{"command": "rm -rf /tmp/x"})

	if got := engine.Evaluate(Request{Tool: "bash", Command: "rm -rf /tmp/x"}); got != Deny {
		t.Fatalf("deny rules stay absolute over session approvals, got %v", got)
	}
}

func TestSessionApprovalMatchesPathTarget(t *testing.T) {
	engine := NewEngine(DefaultConfig()) // write: dangerous-default deny
	engine.AddSessionRule("write", map[string]any{"path": "/tmp/notes.md"})

	if got := engine.Evaluate(Request{Tool: "write", Path: "/tmp/notes.md"}); got != Allow {
		t.Fatalf("exact path should be allowed, got %v", got)
	}
	if got := engine.Evaluate(Request{Tool: "write", Path: "/tmp/other.md"}); got != Deny {
		t.Fatalf("other paths must not be covered, got %v", got)
	}
}
