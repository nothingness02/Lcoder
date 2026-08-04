package events

// GoalUpdated is emitted when the agent's goal record changes status.
const GoalUpdated EventType = "goal_updated"

// GoalUpdatedEvent carries a full snapshot of the goal record after a
// transition, so consumers can render goal state purely event-driven (with
// Goal() only as an initial query). An empty Status means the goal record was
// cleared (CancelGoal) — there is no active goal.
type GoalUpdatedEvent struct {
	Base
	Objective   string `json:"objective"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	TurnBudget  int    `json:"turn_budget"`
	TurnsUsed   int    `json:"turns_used"`
	TokenBudget int    `json:"token_budget"`
	TokensUsed  int    `json:"tokens_used"`
}
