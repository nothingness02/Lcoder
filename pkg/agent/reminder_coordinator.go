package agent

import (
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/task"
)

// reminderCoordinator aggregates ephemeral system reminders for a turn. It
// combines the task manager's standing reminder with any configured reminder
// producers that inspect the conversation.
type reminderCoordinator struct {
	taskMgr   *task.Manager
	producers []ReminderProducer
}

// newReminderCoordinator creates a coordinator from the agent's task manager and
// configured reminder producers.
func newReminderCoordinator(taskMgr *task.Manager, producers []ReminderProducer) *reminderCoordinator {
	if taskMgr == nil {
		taskMgr = task.NewManager()
	}
	return &reminderCoordinator{
		taskMgr:   taskMgr,
		producers: producers,
	}
}

// Reminders returns all ephemeral reminders for the upcoming turn, given the
// current conversation. The task manager reminder always comes first, followed
// by the output of each producer in order.
func (rc *reminderCoordinator) Reminders(msgs []models.AgentMessage) []string {
	var reminders []string
	if r := rc.taskMgr.FormatReminder(); r != "" {
		reminders = append(reminders, r)
	}
	for _, p := range rc.producers {
		reminders = append(reminders, p(msgs)...)
	}
	return reminders
}
