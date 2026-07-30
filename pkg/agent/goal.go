package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
)

// GoalStatus mirrors Kimi Code's goal record lifecycle.
type GoalStatus string

const (
	GoalActive   GoalStatus = "active"   // driver 正在追求
	GoalPaused   GoalStatus = "paused"   // 中断/错误/崩溃恢复,可 resume
	GoalBlocked  GoalStatus = "blocked"  // 预算耗尽或模型判死局,可 resume
	GoalComplete GoalStatus = "complete" // 模型经 update_goal 自标完成
)

// GoalState is the agent-held goal record. The model mutates it ONLY through
// the update_goal tool (applied by the executor); the driver and /goal
// commands mutate it through Agent methods.
type GoalState struct {
	Objective   string
	Status      GoalStatus
	TurnBudget  int // 0 = 不限
	TokenBudget int // 0 = 不限;累计 output token(CompletionTokens)
	TurnsUsed   int
	TokensUsed  int
	BlockReason string
}

// OverBudget reports whether any configured budget is exhausted.
func (g *GoalState) OverBudget() bool {
	if g.TurnBudget > 0 && g.TurnsUsed >= g.TurnBudget {
		return true
	}
	if g.TokenBudget > 0 && g.TokensUsed >= g.TokenBudget {
		return true
	}
	return false
}

// RecordUsage adds one turn's output tokens to the budget ledger. The ONLY
// writer is the run loop at turn boundaries — never event subscribers or
// deciders — so the ledger cannot be double-counted.
func (g *GoalState) RecordUsage(u models.LLMUsage) {
	g.TokensUsed += u.CompletionTokens
}

// goalHolder shares the goal record between the Agent (driver, run loop) and
// the executor (update_goal application), surviving WithMode clones.
type goalHolder struct {
	mu   sync.RWMutex
	goal *GoalState
}

func (h *goalHolder) get() *GoalState {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.goal == nil {
		return nil
	}
	cp := *h.goal
	return &cp
}

func (h *goalHolder) set(g *GoalState) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.goal = g
}

// mutate applies fn to the live goal under lock.
func (h *goalHolder) mutate(fn func(*GoalState)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.goal != nil {
		fn(h.goal)
	}
}

// applyUpdate validates and applies a model-requested transition
// (update_goal tool). Only active → complete/blocked is legal.
func (h *goalHolder) applyUpdate(status, reason string) (GoalStatus, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.goal == nil {
		return "", fmt.Errorf("no active goal; start one with /goal before calling update_goal")
	}
	if h.goal.Status != GoalActive {
		return "", fmt.Errorf("goal is %s, not active; only an active goal can be updated", h.goal.Status)
	}
	switch GoalStatus(status) {
	case GoalComplete:
		h.goal.Status = GoalComplete
	case GoalBlocked:
		if reason == "" {
			return "", fmt.Errorf("reason is required when status=blocked")
		}
		h.goal.Status = GoalBlocked
		h.goal.BlockReason = reason
	default:
		return "", fmt.Errorf("invalid status %q: must be complete or blocked", status)
	}
	return h.goal.Status, nil
}

// Goal returns a copy of the current goal record, or nil.
func (a *Agent) Goal() *GoalState { return a.goals.get() }

// startGoal creates an active goal, replacing any settled one.
func (a *Agent) startGoal(objective string, turnBudget, tokenBudget int) {
	a.goals.set(&GoalState{
		Objective:   objective,
		Status:      GoalActive,
		TurnBudget:  turnBudget,
		TokenBudget: tokenBudget,
	})
}

// pauseGoal marks an active goal paused (interrupt / error / driver exit).
func (a *Agent) pauseGoal(reason string) {
	a.goals.mutate(func(g *GoalState) {
		if g.Status == GoalActive {
			g.Status = GoalPaused
			g.BlockReason = reason
		}
	})
}

// blockGoal marks an active goal blocked with a reason.
func (a *Agent) blockGoal(reason string) {
	a.goals.mutate(func(g *GoalState) {
		if g.Status == GoalActive {
			g.Status = GoalBlocked
			g.BlockReason = reason
		}
	})
}

// goalBudgetVeto is the built-in chain-head decider: an active goal that is
// over budget is a deterministic ceiling — nothing may continue the run.
// Self-guarded: a nil or settled goal passes everything through. It can only
// stop the loop, never continue it (see the chain call site).
func (a *Agent) goalBudgetVeto(_ context.Context, _ StopContext) (bool, error) {
	g := a.goals.get()
	if g == nil || g.Status != GoalActive {
		return true, nil
	}
	if g.OverBudget() {
		return false, nil
	}
	return true, nil
}

// GoalContinuationPromptText is the marker line every continuation prompt
// starts with; tests and the TUI assert on it.
const GoalContinuationPromptText = "Continue working toward the active goal."

// goalContinuationPrompt is the autonomous stand-in for the user typing
// "continue" (ported from kimi-code's GOAL_CONTINUATION_PROMPT). The model
// settles the goal via update_goal; otherwise the driver runs another turn.
const goalContinuationPrompt = GoalContinuationPromptText + ` Keep the self-audit brief. ` +
	`Do not explore unrelated interpretations once the goal can be decided. ` +
	`If the objective is simple, already answered, impossible, unsafe, or contradictory, do not run another goal turn: ` +
	`explain briefly if useful, then call update_goal with complete or blocked in the same turn. ` +
	`Otherwise choose one bounded, useful slice of work. Do not try to finish a broad goal in one turn unless the whole goal is genuinely small. ` +
	`After completing a useful slice with material work remaining, end the turn normally WITHOUT calling update_goal so the runtime continues the goal in the next turn. ` +
	`Call update_goal with complete only when all required work is done and verified: never after only a plan, summary, first pass, or partial result. ` +
	`Call update_goal with blocked only for a genuine impasse that has repeated for at least 3 consecutive goal turns, or an objective that is impossible, unsafe, or contradictory.`

// goalStepCapContinuationPrompt is the variant used when the previous run hit
// MaxTurnsPerRun (kimi-code's GOAL_STEP_CAP_CONTINUATION_PROMPT).
const goalStepCapContinuationPrompt = `The previous goal turn reached the per-turn step limit before finishing its work, ` +
	`so a new turn was started for you. Pick up where that turn stopped and keep each slice of work small enough to fit the limit. ` +
	goalContinuationPrompt

// NextGoalAction decides what the goal driver does after a run ends, given
// the goal record and how the run ended. It is the pure decision core of
// kimi-code's driveGoal post-turn logic, shared by GoalDriver (headless) and
// the TUI continuation wiring. done=true means the pursuit ends here.
func NextGoalAction(g *GoalState, reason events.AgentEndReason) (prompt string, done bool) {
	if g == nil || g.Status != GoalActive {
		return "", true // 模型已用 update_goal 决出终态,或无 goal
	}
	switch reason {
	case events.EndReasonInterrupted, events.EndReasonError:
		return "", true // 调用方负责 pauseGoal
	case events.EndReasonMaxTurns:
		if g.OverBudget() {
			return "", true // 调用方负责 blockGoal
		}
		return goalStepCapContinuationPrompt, false
	default: // completed / terminated:模型说完或显式硬停,都尊重,继续追求
		if g.OverBudget() {
			return "", true
		}
		return goalContinuationPrompt, false
	}
}

// GoalDriver runs ordinary Prompt turns until the goal settles. It is the
// loop-external half of the two-layer design: per-turn safety (max_turns)
// stays inside the run; cross-turn pursuit lives here.
type GoalDriver struct {
	agent *Agent
}

// NewGoalDriver creates a driver for the agent.
func NewGoalDriver(a *Agent) *GoalDriver { return &GoalDriver{agent: a} }

// Run starts a fresh goal and pursues it until it settles (complete/blocked),
// a budget is exhausted (blocked), or the run is interrupted or fails
// (paused).
func (d *GoalDriver) Run(ctx context.Context, objective string, turnBudget, tokenBudget int) error {
	a := d.agent
	a.startGoal(objective, turnBudget, tokenBudget)

	next := objective
	for {
		g := a.Goal()
		if g == nil {
			return nil
		}
		if g.OverBudget() {
			a.blockGoal("a configured budget was reached")
			return nil
		}
		a.goals.mutate(func(live *GoalState) { live.TurnsUsed++ })

		if err := a.Prompt(ctx, models.UserMessage(next)); err != nil {
			a.pauseGoal(err.Error())
			return err
		}
		reason := a.LastEndReason()
		if reason == events.EndReasonInterrupted || reason == events.EndReasonError {
			a.pauseGoal(string(reason))
			return nil
		}

		prompt, done := NextGoalAction(a.Goal(), reason)
		if done {
			g = a.Goal()
			if g != nil && g.Status == GoalActive {
				a.blockGoal("a configured budget was reached")
			}
			return nil
		}
		next = prompt
	}
}
