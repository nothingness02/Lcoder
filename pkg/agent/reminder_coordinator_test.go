package agent

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/task"
)

func TestReminderCoordinatorIncludesTaskReminder(t *testing.T) {
	tm := task.NewManager()
	_, _, _ = tm.ReplaceAll([]task.Task{
		{Text: "step one", Status: task.StatusDone},
		{Text: "step two", Status: task.StatusInProgress},
	})

	rc := newReminderCoordinator(tm, nil)
	reminders := rc.Reminders(nil)

	if len(reminders) != 1 {
		t.Fatalf("expected 1 reminder, got %d", len(reminders))
	}
	if reminders[0] == "" {
		t.Fatal("expected non-empty task reminder")
	}
}

func TestReminderCoordinatorRunsProducers(t *testing.T) {
	tm := task.NewManager()
	rc := newReminderCoordinator(tm, []ReminderProducer{
		func(msgs []models.AgentMessage) []string {
			return []string{"producer one"}
		},
		func(msgs []models.AgentMessage) []string {
			return []string{"producer two"}
		},
	})

	reminders := rc.Reminders(nil)
	if len(reminders) != 2 {
		t.Fatalf("expected 2 reminders, got %d: %v", len(reminders), reminders)
	}
	if reminders[0] != "producer one" || reminders[1] != "producer two" {
		t.Errorf("reminders = %v, want [producer one producer two]", reminders)
	}
}

func TestReminderCoordinatorPassesMessagesToProducers(t *testing.T) {
	tm := task.NewManager()
	msgs := []models.AgentMessage{models.UserMessage("hello")}
	var got []models.AgentMessage
	rc := newReminderCoordinator(tm, []ReminderProducer{
		func(m []models.AgentMessage) []string {
			got = m
			return nil
		},
	})

	rc.Reminders(msgs)
	if len(got) != 1 || got[0].Text() != "hello" {
		t.Errorf("producer received %v, want user message", got)
	}
}

func TestReminderCoordinatorEmptyWhenNoReminders(t *testing.T) {
	tm := task.NewManager()
	rc := newReminderCoordinator(tm, nil)
	reminders := rc.Reminders(nil)
	if len(reminders) != 0 {
		t.Errorf("expected no reminders, got %v", reminders)
	}
}
