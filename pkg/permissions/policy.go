package permissions

import (
	"fmt"
)

// Policy is one link in the permission decision chain. The chain is ordered:
// the first policy that has an opinion decides the request, and policies
// behind it are never consulted.
type Policy interface {
	// Name identifies the policy in explanations and audit output.
	Name() string
	// Decide returns (decision, reason, true) when the policy has an opinion
	// on the request, or ("", "", false) to pass to the next policy. The
	// reason is fed back to the model when the decision blocks the call.
	Decide(req Request) (Decision, string, bool)
}

// The built-in chain, in order. The position of each policy encodes its
// priority relative to the others:
//
//  1. guards (installed via SetGuardPolicies — mode/skill surface
//     constraints; they hold regardless of unsafe mode or user rules)
//  2. unsafePolicy — unsafe mode bypasses rules (ultra-destructive still
//     asks). Note it sits ABOVE deny: unsafe is an explicit everything-goes
//     switch, except for the ultra-destructive list.
//  3. denyRulesPolicy — user deny rules are absolute over ask/allow rules:
//     no allow/ask rule, however specific, can override a matching deny
//  4. sessionApprovalPolicy — exact-match approvals granted earlier in this
//     session; placed after deny (deny stays absolute) and before static
//     ask/allow rules (a session approval beats a static ask rule)
//  5. hookPolicies (installed via SetHookPolicies — extension-provided, e.g.
//     the extension permission hook). Below deny and session approvals so
//     extensions can neither override a deny nor veto an in-session
//     approval; above static user rules so organization policy can win.
//  6. userRulesPolicy — matching ask/allow rules resolved by pattern
//     specificity (longest pattern wins)
//  7. dangerousDefaultPolicy — write/edit/bash with no matching rule are
//     denied, so an omitted config cannot silently allow destructive ops
//  8. fallbackAllowPolicy — everything else is allowed
func (e *Engine) chain() []Policy {
	guards := e.guardPolicies()
	hooks := e.hookPolicySnapshot()
	policies := make([]Policy, 0, len(guards)+len(hooks)+6)
	policies = append(policies, guards...)
	policies = append(policies,
		unsafePolicy{engine: e},
		denyRulesPolicy{engine: e},
		sessionApprovalPolicy{engine: e},
	)
	policies = append(policies, hooks...)
	policies = append(policies,
		userRulesPolicy{engine: e},
		dangerousDefaultPolicy{},
		fallbackAllowPolicy{},
	)
	return policies
}

// unsafePolicy bypasses all rules when unsafe mode is enabled, except that
// ultra-destructive bash commands are still escalated to Ask.
type unsafePolicy struct{ engine *Engine }

func (p unsafePolicy) Name() string { return "unsafe-mode" }

func (p unsafePolicy) Decide(req Request) (Decision, string, bool) {
	if !p.engine.unsafeMode {
		return "", "", false
	}
	if req.Tool == "bash" && req.Command != "" && p.engine.IsUltraDestructive(req.Command) {
		return Ask, "ultra-destructive command requires confirmation even in unsafe mode", true
	}
	return Allow, "unsafe mode bypasses permission rules", true
}

// ruleMatch is one matching rule entry.
type ruleMatch struct {
	pattern  string
	decision Decision
}

// requestTarget returns the string rules are matched against: the command
// for bash, the path for file tools, "*" when neither is set.
func requestTarget(req Request) string {
	if req.Command != "" {
		return req.Command
	}
	if req.Path != "" {
		return req.Path
	}
	return "*"
}

// matchRules returns every rule for the tool that matches the request target
// (command for bash, path otherwise, "*" when neither is set).
func (e *Engine) matchRules(req Request) []ruleMatch {
	table, ok := e.mergedRules(req.Tool)
	if !ok {
		return nil
	}
	target := requestTarget(req)
	isCommand := req.Command != ""
	var out []ruleMatch
	for pattern, decision := range table {
		matched := false
		if isCommand {
			matched = MatchCommand(pattern, target)
		} else {
			matched = e.MatchPathVariants(pattern, target)
		}
		if !matched {
			continue
		}
		out = append(out, ruleMatch{pattern: pattern, decision: decision})
	}
	return out
}

// sessionApprovalPolicy replays exact-match approvals the user granted
// earlier in this session. It runs after user deny rules (deny stays
// absolute) and before static ask/allow rules, so "approved for this
// session" beats a static ask rule but never a deny.
type sessionApprovalPolicy struct{ engine *Engine }

func (p sessionApprovalPolicy) Name() string { return "session-approval" }

func (p sessionApprovalPolicy) Decide(req Request) (Decision, string, bool) {
	if p.engine.hasSessionRule(req) {
		return Allow, "approved earlier in this session", true
	}
	return "", "", false
}

// denyRulesPolicy makes user deny rules absolute: they are evaluated before
// any ask/allow rule, so a specific allow can never punch through a generic
// deny (e.g. deny "rm *" cannot be overridden by allow "rm -rf /tmp/x").
type denyRulesPolicy struct{ engine *Engine }

func (p denyRulesPolicy) Name() string { return "user-deny-rule" }

func (p denyRulesPolicy) Decide(req Request) (Decision, string, bool) {
	for _, m := range p.engine.matchRules(req) {
		if m.decision == Deny {
			return Deny, fmt.Sprintf("denied by rule %q", m.pattern), true
		}
	}
	return "", "", false
}

// userRulesPolicy resolves matching ask/allow rules. When several match, the
// longest (most specific) pattern wins.
type userRulesPolicy struct{ engine *Engine }

func (p userRulesPolicy) Name() string { return "user-rule" }

func (p userRulesPolicy) Decide(req Request) (Decision, string, bool) {
	var best *ruleMatch
	for _, m := range p.engine.matchRules(req) {
		if m.decision != Ask && m.decision != Allow {
			continue
		}
		switch {
		case best == nil:
			m := m
			best = &m
		case len(m.pattern) > len(best.pattern):
			m := m
			best = &m
		case len(m.pattern) == len(best.pattern) && m.decision == Ask && best.decision == Allow:
			// Deterministic tie-break: the conservative decision wins, so the
			// outcome never depends on map iteration order.
			m := m
			best = &m
		}
	}
	if best == nil {
		return "", "", false
	}
	return best.decision, fmt.Sprintf("matched rule %q", best.pattern), true
}

// dangerousDefaultPolicy denies write/edit/bash when no rule matched, so an
// omitted or empty permission config cannot silently allow destructive ops.
type dangerousDefaultPolicy struct{}

func (p dangerousDefaultPolicy) Name() string { return "dangerous-default" }

func (p dangerousDefaultPolicy) Decide(req Request) (Decision, string, bool) {
	if dangerousTools[req.Tool] {
		return Deny, fmt.Sprintf("no permission rule covers %q; denied by default", req.Tool), true
	}
	return "", "", false
}

// fallbackAllowPolicy terminates the chain: anything not covered above is
// allowed (read-only and unknown tools).
type fallbackAllowPolicy struct{}

func (p fallbackAllowPolicy) Name() string { return "default-allow" }

func (p fallbackAllowPolicy) Decide(Request) (Decision, string, bool) {
	return Allow, "", true
}
