package tui

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/task"
)

func sampleTasks() []task.Task {
	return []task.Task{
		{Text: "read auth", Status: task.StatusDone},
		{Text: "split handler", Status: task.StatusInProgress},
	}
}

func TestTaskSidebarVisible(t *testing.T) {
	m := &Model{width: 100, tasks: sampleTasks()}
	if !m.taskSidebarVisible() {
		t.Fatal("sidebar should be visible with tasks on a wide terminal")
	}
	m.taskSidebarHidden = true
	if m.taskSidebarVisible() {
		t.Fatal("sidebar should hide when user toggled it off")
	}
	m.taskSidebarHidden = false
	m.width = 50
	if m.taskSidebarVisible() {
		t.Fatal("sidebar should hide on a narrow terminal")
	}
	m.width = 100
	m.tasks = nil
	if m.taskSidebarVisible() {
		t.Fatal("sidebar should hide with no tasks")
	}
}

func TestMainContentWidth(t *testing.T) {
	// The task list renders as a bottom strip now, so the main content width
	// is always the full terminal width regardless of tasks.
	m := &Model{width: 100, tasks: sampleTasks()}
	if got := m.mainContentWidth(); got != 100 {
		t.Fatalf("main width = %d, want 100", got)
	}
	m.tasks = nil
	if got := m.mainContentWidth(); got != 100 {
		t.Fatalf("main width without tasks = %d, want 100", got)
	}
}

func TestApplyTaskUpdate(t *testing.T) {
	m := &Model{}
	ok := m.applyTaskUpdate(map[string]any{"todos": []any{
		map[string]any{"text": "a", "status": "pending"},
	}})
	if !ok || len(m.tasks) != 1 {
		t.Fatalf("valid update failed: ok=%v tasks=%v", ok, m.tasks)
	}
	if ok := m.applyTaskUpdate(map[string]any{"todos": "garbage"}); ok {
		t.Fatal("malformed update should return false")
	}
	if len(m.tasks) != 1 {
		t.Fatal("malformed update must keep the previous task list")
	}
}

func TestTasksFromMessages(t *testing.T) {
	older := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Name: task.ToolName,
		Arguments: map[string]any{"todos": []any{
			map[string]any{"text": "old", "status": "pending"},
		}},
	})
	newer := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Name: task.ToolName,
		Arguments: map[string]any{"todos": []any{
			map[string]any{"text": "new1", "status": "done"},
			map[string]any{"text": "new2", "status": "pending"},
		}},
	})
	got := tasksFromMessages([]models.AgentMessage{older, newer})
	if len(got) != 2 || got[0].Text != "new1" {
		t.Fatalf("should rebuild from the latest todo_write, got %+v", got)
	}
	if tasksFromMessages(nil) != nil {
		t.Fatal("no messages should yield nil")
	}
}

func TestRenderTaskStrip(t *testing.T) {
	out := renderTaskStrip(sampleTasks(), 40)
	if !strings.Contains(out, "Tasks") {
		t.Fatalf("strip missing header: %q", out)
	}
	if !strings.Contains(out, "read auth") || !strings.Contains(out, "split handler") {
		t.Fatalf("strip missing task text: %q", out)
	}
	if !strings.Contains(out, "1/2") {
		t.Fatalf("strip missing done/total footer: %q", out)
	}
}

func TestRenderTaskStripOverflow(t *testing.T) {
	tasks := make([]task.Task, 8)
	for i := range tasks {
		tasks[i] = task.Task{Text: "task", Status: task.StatusPending}
	}
	out := renderTaskStrip(tasks, 40)
	if !strings.Contains(out, "+3 more") {
		t.Fatalf("strip should collapse overflow, got %q", out)
	}
}

func TestHandleEventTaskListUpdatedUpdatesTasksNoBlock(t *testing.T) {
	m := &Model{width: 100, input: NewInputModel(), agent: &fakeAgent{}}
	before := len(m.blocks)
	m.handleEvent(events.TaskListUpdatedEvent{
		Base: events.Base{Type: events.TaskListUpdated, Turn: 1},
		Tasks: []task.Task{
			{Text: "a", Status: task.StatusInProgress},
		},
	})
	if len(m.tasks) != 1 || m.tasks[0].Status != task.StatusInProgress {
		t.Fatalf("TaskListUpdatedEvent should populate tasks, got %+v", m.tasks)
	}
	if len(m.blocks) != before {
		t.Fatalf("task update must NOT append a conversation block, blocks grew by %d", len(m.blocks)-before)
	}
}

func TestHandleEventOtherToolStillBlocks(t *testing.T) {
	m := &Model{width: 100, input: NewInputModel(), agent: &fakeAgent{}}
	m.handleEvent(events.ToolExecutionStartEvent{
		ToolCallID: "c2",
		ToolName:   "bash",
		Args:       map[string]any{"command": "ls"},
	})
	if len(m.blocks) != 1 {
		t.Fatalf("non-todo tool should append a block, got %d blocks", len(m.blocks))
	}
}
