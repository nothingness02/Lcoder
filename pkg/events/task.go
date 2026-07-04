package events

import "github.com/lcoder/lcoder/pkg/task"

// TaskListUpdated is emitted when the agent's task list changes.
const TaskListUpdated EventType = "task_list_updated"

// TaskListUpdatedEvent carries the new task list.
type TaskListUpdatedEvent struct {
	Base
	Tasks []task.Task `json:"tasks"`
}
