package agent

import (
	"fmt"
	"strings"

	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/skills"
)

// Guard policies express harness constraints — which tools the current
// mode/skill surface exposes — as links in the permission decision chain.
// They run ahead of user rules and unsafe mode, so a mode deny holds no
// matter what the rules say. Enforcement stays at execution time (not schema
// filtering) so the tool array — the first layer of the provider cache
// prefix — stays byte-identical across mode/skill switches.

// modeGuardPolicy enforces the active mode's tool surface restriction
// (allowed_tools / denied_tools / argument-level rules).
type modeGuardPolicy struct{ ex *executor }

func (p modeGuardPolicy) Name() string { return "mode-guard" }

func (p modeGuardPolicy) Decide(req permissions.Request) (permissions.Decision, string, bool) {
	// switch_mode is exempt — the model must always be able to leave the mode.
	if req.Tool == switchModeToolName {
		return "", "", false
	}
	if reason, denied := p.ex.modeDenies(req.Tool); denied {
		return permissions.Deny, reason, true
	}
	for _, rule := range p.ex.currentMode().Rules {
		if !p.matchModeRule(rule, req) {
			continue
		}
		reason := fmt.Sprintf("%s mode rule: %s matching %q is %sed", p.ex.currentMode().Name, rule.Tool, rule.Match, rule.Decision)
		if rule.Decision == "ask" {
			return permissions.Ask, reason, true
		}
		return permissions.Deny, reason, true
	}
	return "", "", false
}

// matchModeRule reports whether a mode rule applies to the request: the tool
// name must match exactly and Match must match the command for bash, the
// path for file tools, or "*" when neither is set. An empty Match covers
// every call of the tool. Path matching uses the same normalized
// path-variant glob as permission rules, so "./x" and "dir/../x" spellings
// cannot bypass a mode rule either.
func (p modeGuardPolicy) matchModeRule(rule ModeRule, req permissions.Request) bool {
	if rule.Tool != req.Tool {
		return false
	}
	if rule.Match == "" {
		return true
	}
	target := req.Command
	isCommand := target != ""
	if target == "" {
		target = req.Path
	}
	if target == "" {
		target = "*"
	}
	if isCommand {
		return permissions.MatchCommand(rule.Match, target)
	}
	if p.ex.permissions != nil {
		return p.ex.permissions.MatchPathVariants(rule.Match, target)
	}
	return permissions.MatchPath(rule.Match, target)
}

// modeTransitionPolicy turns switch_mode out of a mode with
// require_approval_to_exit into an ask, so the user reviews the agent's work
// before it leaves a restricted mode (kimi-code's ExitPlanMode review).
type modeTransitionPolicy struct{ ex *executor }

func (p modeTransitionPolicy) Name() string { return "mode-transition" }

func (p modeTransitionPolicy) Decide(req permissions.Request) (permissions.Decision, string, bool) {
	if req.Tool != switchModeToolName {
		return "", "", false
	}
	target, _ := req.Args["mode"].(string)
	mode := p.ex.currentMode()
	if !mode.RequireApprovalToExit || target == "" || target == mode.Name {
		return "", "", false
	}
	return permissions.Ask, fmt.Sprintf("leaving %s mode to enter %s mode requires approval", mode.Name, target), true
}

// skillGuardPolicy enforces the active skill's tool restriction (SKILL.md
// frontmatter allowed_tools).
type skillGuardPolicy struct{ ex *executor }

func (p skillGuardPolicy) Name() string { return "skill-guard" }

func (p skillGuardPolicy) Decide(req permissions.Request) (permissions.Decision, string, bool) {
	// use_skill is exempt: the model must always be able to activate a
	// different skill, which replaces or lifts the restriction. switch_mode
	// is likewise exempt: it is a meta-tool handled before the tool surface.
	if req.Tool == skills.UseSkillToolName || req.Tool == switchModeToolName {
		return "", "", false
	}
	if !p.ex.skillAllows(req.Tool) {
		return permissions.Deny, fmt.Sprintf("tool %q is restricted by the active skill; allowed tools: %s (plus %s)",
			req.Tool, strings.Join(p.ex.skillFilterNames(), ", "), skills.UseSkillToolName), true
	}
	return "", "", false
}
