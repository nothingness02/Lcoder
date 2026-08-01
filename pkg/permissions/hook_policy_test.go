package permissions

import "testing"

type stubPolicy struct {
	name     string
	decision Decision
	reason   string
}

func (p stubPolicy) Name() string { return p.name }
func (p stubPolicy) Decide(Request) (Decision, string, bool) {
	return p.decision, p.reason, true
}

// extension allow 不得推翻 deny 规则(deny 绝对)。
func TestHookPolicyAllowDoesNotOverrideDeny(t *testing.T) {
	eng := NewEngineFromRules([]Rule{
		{Tool: "bash", Pattern: "rm -rf /*", Decision: Deny},
	})
	eng.SetHookPolicies(stubPolicy{name: "ext", decision: Allow, reason: "ext says fine"})

	d, policy, _ := eng.DecideWithSource("bash", map[string]any{"command": "rm -rf /"})
	if d != Deny {
		t.Fatalf("extension allow must not override deny, got %v (%s)", d, policy)
	}
}

// extension deny 依然生效(在无规则处)。
func TestHookPolicyDenyApplies(t *testing.T) {
	eng := NewEngineFromRules(nil)
	eng.SetHookPolicies(stubPolicy{name: "ext", decision: Deny, reason: "org policy"})

	d, _, reason := eng.DecideWithSource("bash", map[string]any{"command": "make deploy"})
	if d != Deny || reason != "org policy" {
		t.Fatalf("extension deny must apply, got %v (%s)", d, reason)
	}
}

// session 批准优先于 extension 决定(用户本会话的显式信任不被否决)。
func TestSessionApprovalBeatsHookPolicy(t *testing.T) {
	eng := NewEngineFromRules(nil)
	eng.SetHookPolicies(stubPolicy{name: "ext", decision: Deny, reason: "org policy"})
	eng.AddSessionRule("bash", map[string]any{"command": "make deploy"})

	d, policy, _ := eng.DecideWithSource("bash", map[string]any{"command": "make deploy"})
	if d != Allow {
		t.Fatalf("session approval must beat extension decision, got %v (%s)", d, policy)
	}
}
