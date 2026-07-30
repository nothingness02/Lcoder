package builtin

import (
	"context"
	"fmt"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/task"
	"github.com/lcoder/lcoder/pkg/tools"
)

// TodoWrite is a stateless tool the model calls to declare/update its task list.
// It validates the payload and returns a one-line summary; the TUI derives the
// visible task sidebar from the tool call's args (see pkg/tui/tasksidebar.go).
type TodoWrite struct{}

// NewTodoWrite builds the todo_write tool. cwd is unused (no filesystem access).
func NewTodoWrite(cwd string) tools.Executable { return &TodoWrite{} }

func (t *TodoWrite) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name: task.ToolName,
		Description: "Declare or update your task list for a multi-step job. " +
			"Call this when a request needs several steps: list each task with a status. " +
			"Mark a task in_progress before you start it and done when finished. " +
			"Always pass the COMPLETE list every call — it replaces the previous list. " +
			"Skip this for trivial single-step requests.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"todos": map[string]any{
					"type":        "array",
					"description": "The full task list, in order.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"text": map[string]any{
								"type":        "string",
								"description": "Short imperative task description.",
							},
							"status": map[string]any{
								"type": "string",
								"enum": []any{"pending", "in_progress", "done"},
							},
						},
						"required": []any{"text", "status"},
					},
				},
			},
			"required": []any{"todos"},
		},
	}
}

func (t *TodoWrite) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	tasks, err := task.Parse(args["todos"])
	if err != nil {
		return models.ToolExecutionResult{}, err
	}
	done, inProgress, pending := task.Counts(tasks)
	summary := fmt.Sprintf("Updated %d tasks: %d done, %d in progress, %d pending",
		len(tasks), done, inProgress, pending)
	return models.NewToolExecutionResultText(summary), nil
}

var _ tools.Executable = (*TodoWrite)(nil)
