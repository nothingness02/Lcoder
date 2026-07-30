package events

// GoalUpdated is emitted when the agent's goal record changes status.
const GoalUpdated EventType = "goal_updated"

// GoalUpdatedEvent carries the goal transition.
type GoalUpdatedEvent struct {
	Base
	Objective string `json:"objective"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
}
