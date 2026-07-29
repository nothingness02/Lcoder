package tui

import (
	"fmt"
	"strings"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/task"
)

// taskSidebarVisible reports whether the task strip should render above the
// composer: there are tasks and the user hasn't hidden it.
func (m *Model) taskSidebarVisible() bool {
	return len(m.tasks) > 0 && !m.taskSidebarHidden && m.width >= 60
}

// mainContentWidth is the width available to the conversation/composer. The
// task list renders as a bottom strip now, so no column is subtracted.
func (m *Model) mainContentWidth() int {
	return m.width
}

// applyTaskUpdate parses a todo_write tool's args into the task list. It returns
// true when the list was replaced, false when the args were malformed (the
// previous list is kept).
func (m *Model) applyTaskUpdate(args map[string]any) bool {
	tasks, err := task.Parse(args["todos"])
	if err != nil {
		return false
	}
	m.tasks = tasks
	return true
}

// toggleTaskSidebar flips the user's hide override and re-lays-out.
func (m *Model) toggleTaskSidebar() {
	m.taskSidebarHidden = !m.taskSidebarHidden
	m.updateSizes()
}

// tasksFromMessages rebuilds the task list from history by finding the most
// recent todo_write tool call. Returns nil when none is present.
func tasksFromMessages(msgs []models.AgentMessage) []task.Task {
	for i := len(msgs) - 1; i >= 0; i-- {
		for _, tc := range msgs[i].ToolCalls() {
			if tc.Name == task.ToolName {
				if tasks, err := task.Parse(tc.Arguments["todos"]); err == nil {
					return tasks
				}
			}
		}
	}
	return nil
}

// taskGlyph maps a status to its sidebar marker.
func taskGlyph(s task.Status) string {
	switch s {
	case task.StatusDone:
		return styleSuccess().Render("✓")
	case task.StatusInProgress:
		return styleAccent().Render("▸")
	default:
		return styleDim().Render("○")
	}
}

// taskStripMaxItems caps the tasks shown in the strip; the rest collapse
// into an overflow count (kimi-code's TodoPanel).
const taskStripMaxItems = 5

// renderTaskStrip draws the task list as a bottom strip above the composer
// (kimi-code's TodoPanel): a header, up to taskStripMaxItems items, and an
// overflow/count footer.
func renderTaskStrip(tasks []task.Task, width int) string {
	var lines []string
	lines = append(lines, styleAccent().Bold(true).Render("Tasks"))
	shown := tasks
	if len(shown) > taskStripMaxItems {
		shown = shown[:taskStripMaxItems]
	}
	for _, t := range shown {
		text := truncateCells(t.Text, width-8, "…")
		lines = append(lines, taskGlyph(t.Status)+" "+text)
	}
	done, pending, inProgress := task.Counts(tasks)
	if extra := len(tasks) - len(shown); extra > 0 {
		lines = append(lines, styleDim().Render(fmt.Sprintf("… +%d more (%d done · %d pending · %d in progress)", extra, done, pending, inProgress)))
	} else {
		lines = append(lines, styleDim().Render(fmt.Sprintf("%d/%d 完成", done, len(tasks))))
	}
	return strings.Join(lines, "\n")
}
