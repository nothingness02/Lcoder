package agentapi

import "github.com/lcoder/lcoder/pkg/models"

type GoalStatus string

const (
	GoalActive   GoalStatus = "active"   // driver 正在追求
	GoalPaused   GoalStatus = "paused"   // 中断/错误/崩溃恢复,可 resume
	GoalBlocked  GoalStatus = "blocked"  // 预算耗尽或模型判死局,可 resume
	GoalComplete GoalStatus = "complete" // 模型经 update_goal 自标完成
)

// GoalState is the agent-held goal record. The model mutates it ONLY through
// the update_goal tool (applied by the executor); the driver and /goal
// commands mutate it through Agent methods. The snake_case json tags are the
// wire projection (rpcserver serializes it directly).
type GoalState struct {
	Objective   string     `json:"objective"`
	Status      GoalStatus `json:"status"`
	TurnBudget  int        `json:"turn_budget"`  // 0 = 不限
	TokenBudget int        `json:"token_budget"` // 0 = 不限;累计 output token(CompletionTokens)
	TurnsUsed   int        `json:"turns_used"`
	TokensUsed  int        `json:"tokens_used"`
	BlockReason string     `json:"block_reason,omitempty"`
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
