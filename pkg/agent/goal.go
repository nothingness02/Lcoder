package agent

import (
	"context"
	"fmt"
	"sync"

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
