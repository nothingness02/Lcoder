package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/subagent"
	"github.com/lcoder/lcoder/pkg/tools"
)

// Subagent delegates work to other Lcoder agents running in isolated subprocesses.
type Subagent struct {
	cwd    string
	runner subagent.Runner
}

// NewSubagent creates a subagent tool bound to cwd with a pre-built runner.
func NewSubagent(cwd string, runner subagent.Runner) tools.Executable {
	return &Subagent{cwd: cwd, runner: runner}
}

// Definition returns the JSON schema for the subagent tool.
func (s *Subagent) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name:        "subagent",
		Description: "Delegate work to other Lcoder agents. Supports a single agent, parallel agents, or a sequential chain where later steps can reference {previous}.",
		Parameters: map[string]any{
			"type": "object",
			"oneOf": []map[string]any{
				{
					"title": "Single",
					"properties": map[string]any{
						"agent": map[string]any{
							"type":        "string",
							"description": "Name of the subagent to run",
						},
						"task": map[string]any{
							"type":        "string",
							"description": "Task to delegate to the subagent",
						},
						"cwd": map[string]any{
							"type":        "string",
							"description": "Working directory for the subagent (defaults to project root)",
						},
					},
					"required": []string{"agent", "task"},
				},
				{
					"title": "Parallel",
					"properties": map[string]any{
						"tasks": map[string]any{
							"type":        "array",
							"description": "Tasks to run in parallel",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"agent": map[string]any{"type": "string"},
									"task":  map[string]any{"type": "string"},
									"cwd":   map[string]any{"type": "string"},
								},
								"required": []string{"agent", "task"},
							},
						},
					},
					"required": []string{"tasks"},
				},
				{
					"title": "Chain",
					"properties": map[string]any{
						"chain": map[string]any{
							"type":        "array",
							"description": "Steps to run sequentially; later steps can reference {previous}",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"agent": map[string]any{"type": "string"},
									"task":  map[string]any{"type": "string"},
									"cwd":   map[string]any{"type": "string"},
								},
								"required": []string{"agent", "task"},
							},
						},
					},
					"required": []string{"chain"},
				},
			},
		},
		ExecutionMode: models.ExecutionSequential,
	}
}

// Execute routes to single, parallel, or chain subagent invocations.
func (s *Subagent) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	_ = callID

	hasSingle := nonEmptyString(args["agent"])
	hasParallel := hasItemSlice(args["tasks"])
	hasChain := hasItemSlice(args["chain"])

	modeCount := 0
	if hasSingle {
		modeCount++
	}
	if hasParallel {
		modeCount++
	}
	if hasChain {
		modeCount++
	}
	if modeCount != 1 {
		return models.ToolExecutionResult{}, fmt.Errorf("subagent: exactly one of agent, tasks, or chain must be provided")
	}

	if hasSingle {
		agentName := args["agent"].(string)
		task, _ := args["task"].(string)
		if strings.TrimSpace(task) == "" {
			return models.ToolExecutionResult{}, fmt.Errorf("subagent: task is required")
		}
		cwd, _ := args["cwd"].(string)
		out, err := s.runner.RunSingle(ctx, agentName, task, cwd)
		if err != nil {
			return models.ToolExecutionResult{}, fmt.Errorf("subagent: single: %w", err)
		}
		return models.NewToolExecutionResultText(out), nil
	}

	if hasParallel {
		items, err := parseSubagentItems(args["tasks"].([]any))
		if err != nil {
			return models.ToolExecutionResult{}, err
		}
		results, err := s.runner.RunParallel(ctx, items)
		if err != nil {
			return models.ToolExecutionResult{}, fmt.Errorf("subagent: parallel: %w", err)
		}
		return models.NewToolExecutionResultText(formatSubagentResults(results)), nil
	}

	items, err := parseSubagentItems(args["chain"].([]any))
	if err != nil {
		return models.ToolExecutionResult{}, err
	}
	out, err := s.runner.RunChain(ctx, items)
	if err != nil {
		return models.ToolExecutionResult{}, fmt.Errorf("subagent: chain: %w", err)
	}
	return models.NewToolExecutionResultText(out), nil
}

func nonEmptyString(v any) bool {
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) != ""
}

func hasItemSlice(v any) bool {
	_, ok := v.([]any)
	return ok
}

func parseSubagentItems(items []any) ([]subagent.TaskItem, error) {
	out := make([]subagent.TaskItem, 0, len(items))
	for i, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("subagent: item %d is not an object", i)
		}
		agentName, _ := m["agent"].(string)
		if strings.TrimSpace(agentName) == "" {
			return nil, fmt.Errorf("subagent: item %d missing agent", i)
		}
		task, _ := m["task"].(string)
		if strings.TrimSpace(task) == "" {
			return nil, fmt.Errorf("subagent: item %d missing task", i)
		}
		cwd, _ := m["cwd"].(string)
		out = append(out, subagent.TaskItem{Agent: agentName, Task: task, CWD: cwd})
	}
	return out, nil
}

func formatSubagentResults(results []subagent.Result) string {
	parts := make([]string, 0, len(results))
	for i, r := range results {
		if r.Err != nil {
			parts = append(parts, fmt.Sprintf("[%d] ERROR: %s", i, r.Err.Error()))
		} else {
			parts = append(parts, fmt.Sprintf("[%d] %s", i, r.Text))
		}
	}
	return strings.Join(parts, "\n\n")
}

var _ tools.Executable = (*Subagent)(nil)
