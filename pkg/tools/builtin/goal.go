package builtin

import (
	"context"
	"fmt"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/tools"
)

// UpdateGoalToolName is the tool the model calls to settle the active goal.
const UpdateGoalToolName = "update_goal"

// UpdateGoal is an inert tool: it validates and echoes the requested
// transition; the executor applies it to the agent's goal record (same
// pattern as todo_write → task.Manager reconcile).
type UpdateGoal struct {
	cwd string
}

// NewUpdateGoal creates the update_goal tool.
func NewUpdateGoal(cwd string) tools.Executable {
	return &UpdateGoal{cwd: cwd}
}

func (u *UpdateGoal) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name: UpdateGoalToolName,
		Description: "Settle the active goal. Call with status=complete ONLY when every requirement of the " +
			"objective is verifiably done (a plan, summary, or partial result is NOT complete). Call with " +
			"status=blocked only for a genuine impasse (missing input, credentials, permissions, or a " +
			"persistent failure) that has repeated across goal turns.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status": map[string]any{
					"type":        "string",
					"enum":        []string{"complete", "blocked"},
					"description": "complete = objective fully done; blocked = genuine impasse",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Required when status=blocked: the blocking condition.",
				},
			},
			"required": []string{"status"},
		},
	}
}

func (u *UpdateGoal) Execute(_ context.Context, _ string, args map[string]any) (models.ToolExecutionResult, error) {
	status, err := tools.RequiredString(args, "status")
	if err != nil {
		return models.ToolExecutionResult{}, err
	}
	if status != "complete" && status != "blocked" {
		return models.ToolExecutionResult{}, fmt.Errorf("invalid status %q: must be complete or blocked", status)
	}
	reason := tools.String(args, "reason", "")
	if status == "blocked" && reason == "" {
		return models.ToolExecutionResult{}, fmt.Errorf("reason is required when status=blocked")
	}
	return models.NewToolExecutionResultText("goal update requested: " + status), nil
}
