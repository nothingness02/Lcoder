package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/tools"
)

type stubConfirm struct {
	allow bool
	scope ConfirmScope
	calls int
}

func (s *stubConfirm) Confirm(context.Context, ToolCallInfo) (bool, error) {
	s.calls++
	return s.allow, nil
}

func (s *stubConfirm) ConfirmWithScope(context.Context, ToolCallInfo) (ConfirmResult, error) {
	s.calls++
	return ConfirmResult{Allow: s.allow, Scope: s.scope}, nil
}

// newGuardTestExecutor builds an executor in the given mode with bash/write
// fake tools and a permission engine that allows everything, so only the
// mode guards gate the calls.
func newGuardTestExecutor(t *testing.T, mode ModeConfig, confirm UserConfirmation) *executor {
	t.Helper()
	mm := NewModeManager()
	mm.modes[mode.Name] = mode
	if _, ok := mm.modes["code"]; !ok {
		mm.modes["code"] = ModeConfig{Name: "code"}
	}

	r := tools.NewRegistry(".")
	r.Register("bash", fakeTool{fullDef("bash", "Run command.")})
	r.Register("write", fakeTool{fullDef("write", "Write file.")})

	engine := permissions.NewEngineFromRules([]permissions.Rule{
		{Tool: "bash", Pattern: "*", Decision: permissions.Allow},
		{Tool: "write", Pattern: "*", Decision: permissions.Allow},
	})

	cfg := Config{Mode: mode.Name, ModeManager: mm, UserConfirm: confirm}
	return &executor{
		cfg:         &cfg,
		mgr:         contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192}),
		registry:    r,
		permissions: engine,
		emitter:     &eventEmitter{bus: events.New()},
		dedup:       make(map[string]models.AgentMessage),
	}
}

func TestModeRuleDeniesMatchingCommand(t *testing.T) {
	e := newGuardTestExecutor(t, ModeConfig{
		Name:  "code",
		Rules: []ModeRule{{Tool: "bash", Match: "git push *", Decision: "deny"}},
	}, nil)

	res := callTool(e, "bash", map[string]any{"path": ".", "command": "git push origin main"})
	if !res.IsError {
		t.Fatal("expected mode rule to deny git push")
	}
	if !strings.Contains(res.Text(), "mode rule") {
		t.Fatalf("denial should name the mode rule, got %q", res.Text())
	}

	// Non-matching commands of the same tool still go through.
	if res = callTool(e, "bash", map[string]any{"path": ".", "command": "git status"}); res.IsError {
		t.Fatalf("git status should be allowed, got %q", res.Text())
	}
}

func TestModeRuleAskTriggersConfirmation(t *testing.T) {
	confirm := &stubConfirm{allow: false}
	e := newGuardTestExecutor(t, ModeConfig{
		Name:  "code",
		Rules: []ModeRule{{Tool: "write", Match: "*.md", Decision: "ask"}},
	}, confirm)

	res := callTool(e, "write", map[string]any{"path": "README.md"})
	if !res.IsError {
		t.Fatal("expected write to be blocked when the user rejects")
	}
	if confirm.calls != 1 {
		t.Fatalf("expected one confirmation round-trip, got %d", confirm.calls)
	}

	// A path outside the rule's match never asks.
	if res = callTool(e, "write", map[string]any{"path": "main.go"}); res.IsError {
		t.Fatalf("write outside the rule match should proceed, got %q", res.Text())
	}
	if confirm.calls != 1 {
		t.Fatalf("non-matching call must not ask, got %d confirmations", confirm.calls)
	}
}

func TestModeTransitionRequiresApproval(t *testing.T) {
	confirm := &stubConfirm{allow: false}
	e := newGuardTestExecutor(t, ModeConfig{
		Name:                  "plan",
		RequireApprovalToExit: true,
	}, confirm)

	res := callTool(e, switchModeToolName, map[string]any{"mode": "code"})
	if !res.IsError {
		t.Fatal("expected switch to be blocked when the user rejects")
	}
	if confirm.calls != 1 {
		t.Fatalf("expected one confirmation, got %d", confirm.calls)
	}
	if mode := e.cfg.Mode; mode != "plan" {
		t.Fatalf("mode must stay plan after rejection, got %q", mode)
	}

	// Switching to the current mode is a no-op that never asks.
	if res = callTool(e, switchModeToolName, map[string]any{"mode": "plan"}); res.IsError {
		t.Fatalf("switching to the current mode should not ask, got %q", res.Text())
	}
	if confirm.calls != 1 {
		t.Fatalf("same-mode switch must not ask, got %d confirmations", confirm.calls)
	}

	confirm.allow = true
	if res = callTool(e, switchModeToolName, map[string]any{"mode": "code"}); res.IsError {
		t.Fatalf("switch should proceed after approval, got %q", res.Text())
	}
	if mode := e.cfg.Mode; mode != "code" {
		t.Fatalf("expected mode switched to code, got %q", mode)
	}
}

func TestModeTransitionFreeWithoutFlag(t *testing.T) {
	confirm := &stubConfirm{allow: false}
	e := newGuardTestExecutor(t, ModeConfig{Name: "plan"}, confirm)

	if res := callTool(e, switchModeToolName, map[string]any{"mode": "code"}); res.IsError {
		t.Fatalf("modes without require_approval_to_exit switch freely, got %q", res.Text())
	}
	if confirm.calls != 0 {
		t.Fatalf("no confirmation expected, got %d", confirm.calls)
	}
}

func TestModeRuleValidation(t *testing.T) {
	mm := NewModeManager()
	err := mm.loadModeData([]byte("name: x\nrules:\n  - tool: bash\n    decision: block\n"), "x.yaml")
	if err == nil || !strings.Contains(err.Error(), "decision") {
		t.Fatalf("invalid decision should be rejected at load, got %v", err)
	}
	err = mm.loadModeData([]byte("name: y\nrules:\n  - match: \"*\"\n    decision: deny\n"), "y.yaml")
	if err == nil || !strings.Contains(err.Error(), "tool") {
		t.Fatalf("missing tool should be rejected at load, got %v", err)
	}
}
