package agent

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/tools"
)

// newSessionTestExecutor builds an executor whose permission engine holds the
// given rules, so tests control exactly which calls ask.
func newSessionTestExecutor(t *testing.T, mode ModeConfig, rules []permissions.Rule, confirm UserConfirmation) *executor {
	t.Helper()
	mm := NewModeManager()
	mm.modes[mode.Name] = mode
	mm.modes["code"] = ModeConfig{Name: "code"}

	r := tools.NewRegistry(".")
	r.Register("write", fakeTool{fullDef("write", "Write file.")})

	cfg := Config{Mode: mode.Name, ModeManager: mm, UserConfirm: confirm}
	return &executor{
		cfg:         &cfg,
		mgr:         contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192}),
		registry:    r,
		permissions: permissions.NewEngineFromRules(rules),
		emitter:     &eventEmitter{bus: events.New()},
		dedup:       make(map[string]models.AgentMessage),
	}
}

// A Session-scoped approval remembers the exact call for the rest of the
// session: the same call never asks again, a different one still does.
func TestSessionApprovalLearnsExactCall(t *testing.T) {
	confirm := &stubConfirm{allow: true, scope: ScopeSession}
	e := newSessionTestExecutor(t, ModeConfig{Name: "code"}, []permissions.Rule{
		{Tool: "write", Pattern: "*", Decision: permissions.Ask},
	}, confirm)

	if res := callTool(e, "write", map[string]any{"path": "a.txt"}); res.IsError {
		t.Fatalf("first call should proceed after approval: %q", res.Text())
	}
	if confirm.calls != 1 {
		t.Fatalf("first call must ask, got %d confirmations", confirm.calls)
	}

	if res := callTool(e, "write", map[string]any{"path": "a.txt"}); res.IsError {
		t.Fatalf("same call should be session-approved: %q", res.Text())
	}
	if confirm.calls != 1 {
		t.Fatalf("session-approved call must not ask again, got %d confirmations", confirm.calls)
	}

	if res := callTool(e, "write", map[string]any{"path": "b.txt"}); res.IsError {
		t.Fatalf("different path should proceed after approval: %q", res.Text())
	}
	if confirm.calls != 2 {
		t.Fatalf("session approval must not generalize to other paths, got %d confirmations", confirm.calls)
	}
}

// learnRule must skip switch_mode entirely: a remembered approval can never
// bypass mode-transition approval.
func TestLearnRuleSkipsSwitchMode(t *testing.T) {
	engine := permissions.NewEngineFromRules(nil)
	e := &executor{permissions: engine}

	e.learnRule(ToolCallInfo{
		ToolCall: models.ToolCallContent{Name: switchModeToolName},
		Args:     map[string]any{"mode": "code"},
	}, ScopeSession)

	_, policy, _ := engine.EvaluateWithSource(permissions.RequestFor(switchModeToolName, map[string]any{"mode": "code"}))
	if policy == "session-approval" {
		t.Fatal("switch_mode must never be learned as a session approval")
	}
}

// Even if a session approval for switch_mode existed, the transition guard
// runs earlier in the chain and still asks.
func TestSessionApprovalCannotBypassTransitionAsk(t *testing.T) {
	confirm := &stubConfirm{allow: true, scope: ScopeOnce}
	e := newSessionTestExecutor(t, ModeConfig{
		Name:                  "plan",
		RequireApprovalToExit: true,
	}, nil, confirm)
	e.permissions.AddSessionRule(switchModeToolName, nil)

	res := callTool(e, switchModeToolName, map[string]any{"mode": "code"})
	if res.IsError {
		t.Fatalf("switch should proceed after approval, got %q", res.Text())
	}
	if confirm.calls != 1 {
		t.Fatalf("transition guard must still ask despite the session approval, got %d confirmations", confirm.calls)
	}
}

// A denied-by-user rejection carries a clear reason back to the model.
func TestDeniedByUserReason(t *testing.T) {
	confirm := &stubConfirm{allow: false}
	e := newSessionTestExecutor(t, ModeConfig{Name: "code"}, []permissions.Rule{
		{Tool: "write", Pattern: "*", Decision: permissions.Ask},
	}, confirm)

	res := callTool(e, "write", map[string]any{"path": "a.txt"})
	if !res.IsError {
		t.Fatal("rejected call must error")
	}
	if !strings.Contains(res.Text(), "denied by user") {
		t.Fatalf("rejection reason should reach the model, got %q", res.Text())
	}
}
