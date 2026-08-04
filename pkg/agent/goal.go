package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/lcoder/lcoder/pkg/agentapi"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
)

// Goal status types and the goal record live in the protocol package
// (pkg/agentapi) so CoreAPI can expose them; these aliases keep the agent
// package's internal code and all existing call sites unchanged.
type GoalStatus = agentapi.GoalStatus

const (
	GoalActive   = agentapi.GoalActive   // driver 正在追求
	GoalPaused   = agentapi.GoalPaused   // 中断/错误/崩溃恢复,可 resume
	GoalBlocked  = agentapi.GoalBlocked  // 预算耗尽或模型判死局,可 resume
	GoalComplete = agentapi.GoalComplete // 模型经 update_goal 自标完成
)

// GoalState is the agent-held goal record.
type GoalState = agentapi.GoalState

// goalHolder shares the goal record between the Agent (driver, run loop) and
// the executor (update_goal application), surviving WithMode clones.
//
// It is also the single emission point for GoalUpdatedEvent: every mutation
// path (set/mutate/applyUpdate) compares the goal snapshot before and after
// the change and notifies onChange only when it actually changed, so no-op
// transitions (PauseGoal on a complete goal, zero-usage accounting, ...) do
// not spam subscribers with identical snapshots. The exception is set(nil):
// CancelGoal always emits the cleared (nil) snapshot. The owning Agent wires
// onChange to its event emitter in New and rewires it in WithMode.
type goalHolder struct {
	mu   sync.RWMutex
	goal *GoalState
	// onChange, when non-nil, is invoked outside the lock after every
	// effective mutation with a snapshot of the goal (nil after CancelGoal).
	onChange func(*GoalState)
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
	before := h.goal
	h.goal = g
	h.mu.Unlock()
	if g == nil {
		// CancelGoal always emits, so subscribers reliably see the clear.
		h.notify()
		return
	}
	if before != nil && *before == *g {
		return // no observable change
	}
	h.notify()
}

// mutate applies fn to the live goal under lock.
func (h *goalHolder) mutate(fn func(*GoalState)) {
	h.mu.Lock()
	if h.goal == nil {
		h.mu.Unlock()
		return
	}
	before := *h.goal
	fn(h.goal)
	changed := *h.goal != before
	h.mu.Unlock()
	if changed {
		h.notify()
	}
}

// notify emits a snapshot of the current goal to onChange, if wired.
func (h *goalHolder) notify() {
	if h.onChange != nil {
		h.onChange(h.get())
	}
}

// applyUpdate validates and applies a model-requested transition
// (update_goal tool). Only active → complete/blocked is legal.
func (h *goalHolder) applyUpdate(status, reason string) (GoalStatus, error) {
	h.mu.Lock()
	if h.goal == nil {
		h.mu.Unlock()
		return "", fmt.Errorf("no active goal; start one with /goal before calling update_goal")
	}
	if h.goal.Status != GoalActive {
		h.mu.Unlock()
		return "", fmt.Errorf("goal is %s, not active; only an active goal can be updated", h.goal.Status)
	}
	before := *h.goal
	switch GoalStatus(status) {
	case GoalComplete:
		h.goal.Status = GoalComplete
	case GoalBlocked:
		if reason == "" {
			h.mu.Unlock()
			return "", fmt.Errorf("reason is required when status=blocked")
		}
		h.goal.Status = GoalBlocked
		h.goal.BlockReason = reason
	default:
		h.mu.Unlock()
		return "", fmt.Errorf("invalid status %q: must be complete or blocked", status)
	}
	newStatus := h.goal.Status
	changed := *h.goal != before
	h.mu.Unlock()
	if changed {
		h.notify()
	}
	return newStatus, nil
}

// Goal returns a copy of the current goal record, or nil.
func (a *Agent) Goal() *GoalState { return a.goals.get() }

// emitGoalUpdated is the goalHolder onChange hook: it converts the post-change
// snapshot into a GoalUpdatedEvent on the agent's bus. A nil snapshot means the
// record was cleared (CancelGoal); the event carries an empty Status.
func (a *Agent) emitGoalUpdated(g *GoalState) {
	turn := 0
	if a.loopState != nil {
		turn = a.loopState.Turn()
	}
	ev := events.GoalUpdatedEvent{
		Base: events.Base{Type: events.GoalUpdated, Turn: turn},
	}
	if g != nil {
		ev.Objective = g.Objective
		ev.Status = string(g.Status)
		ev.Reason = g.BlockReason
		ev.TurnBudget = g.TurnBudget
		ev.TurnsUsed = g.TurnsUsed
		ev.TokenBudget = g.TokenBudget
		ev.TokensUsed = g.TokensUsed
	}
	a.emit(context.Background(), ev)
}

// StartGoal creates an active goal, replacing any settled one.
func (a *Agent) StartGoal(objective string, turnBudget, tokenBudget int) {
	a.goals.set(&GoalState{
		Objective:   objective,
		Status:      GoalActive,
		TurnBudget:  turnBudget,
		TokenBudget: tokenBudget,
	})
}

// PauseGoal marks an active goal paused (interrupt / error / driver exit).
func (a *Agent) PauseGoal(reason string) {
	a.goals.mutate(func(g *GoalState) {
		if g.Status == GoalActive {
			g.Status = GoalPaused
			g.BlockReason = reason
		}
	})
}

// ResumeGoal reactivates a paused or blocked goal.
func (a *Agent) ResumeGoal() {
	a.goals.mutate(func(g *GoalState) {
		if g.Status == GoalPaused || g.Status == GoalBlocked {
			g.Status = GoalActive
			g.BlockReason = ""
		}
	})
}

// CancelGoal clears the goal record.
func (a *Agent) CancelGoal() { a.goals.set(nil) }

// BlockGoal marks an active goal blocked with a reason. It is the exported
// form of blockGoal for goal drivers living outside this package (pkg/host);
// the in-package GoalDriver keeps using blockGoal.
func (a *Agent) BlockGoal(reason string) { a.blockGoal(reason) }

// NoteGoalTurn records one driver-initiated pursuit turn against the goal's
// turn budget. It is the exported form of the increment the in-package
// GoalDriver performs per iteration, for drivers living outside this package
// (pkg/host).
func (a *Agent) NoteGoalTurn() {
	a.goals.mutate(func(live *GoalState) { live.TurnsUsed++ })
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
	a.StartGoal(objective, turnBudget, tokenBudget)

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
			a.PauseGoal(err.Error())
			return err
		}
		reason := a.LastEndReason()
		if reason == events.EndReasonInterrupted || reason == events.EndReasonError {
			a.PauseGoal(string(reason))
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
