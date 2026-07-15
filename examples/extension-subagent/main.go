// Package main implements the Lcoder subagent extension as a Go plugin.
// It registers a "subagent" tool that can delegate work to other Lcoder agents.
package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/lcoder/lcoder/pkg/extension"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/observability"
	"github.com/lcoder/lcoder/pkg/subagent"
	"github.com/lcoder/lcoder/pkg/tools"
)

func main() {
	// Placeholder so `go build` succeeds for this importable extension.
	// The plugin is loaded via the exported New symbol.
}

// New is the plugin entry point expected by Lcoder extension loaders.
func New(cfg map[string]any) (extension.Extension, error) {
	_ = cfg
	return &subagentExtension{
		newRunner: subagent.NewRunner,
	}, nil
}

type subagentExtension struct {
	newRunner func(projectRoot string) (subagent.Runner, error)
}

func (e *subagentExtension) Name() string { return "subagent" }

func (e *subagentExtension) RegisterTools(registry *tools.Registry, cwd string) error {
	runner, err := e.newRunner(cwd)
	if err != nil {
		return fmt.Errorf("subagent: create runner: %w", err)
	}

	registry.Register("subagent", &subagentTool{runner: runner})
	return nil
}

func (e *subagentExtension) RegisterHooks() (extension.Hooks, error) {
	return extension.Hooks{}, nil
}

func (e *subagentExtension) RegisterExporters() (map[string]observability.ExporterFactory, error) {
	return nil, nil
}

type subagentTool struct {
	runner subagent.Runner
}

func (t *subagentTool) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name:        "subagent",
		Description: "Delegate work to other Lcoder agents (single, parallel, or chain).",
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
							"description": "Task to delegate",
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
	}
}

func (t *subagentTool) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	_ = callID

	hasSingle := false
	if name, ok := args["agent"].(string); ok && name != "" {
		hasSingle = true
	}
	hasParallel := false
	if _, ok := args["tasks"].([]any); ok {
		hasParallel = true
	}
	hasChain := false
	if _, ok := args["chain"].([]any); ok {
		hasChain = true
	}

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
		out, err := t.runner.RunSingle(ctx, agentName, task, cwd)
		if err != nil {
			return models.ToolExecutionResult{}, fmt.Errorf("subagent: single: %w", err)
		}
		return models.NewToolExecutionResultText(out), nil
	}

	if hasParallel {
		items, err := parseItems(args["tasks"].([]any))
		if err != nil {
			return models.ToolExecutionResult{}, err
		}
		results, err := t.runner.RunParallel(ctx, items)
		if err != nil {
			return models.ToolExecutionResult{}, fmt.Errorf("subagent: parallel: %w", err)
		}
		return models.NewToolExecutionResultText(formatParallelResults(results)), nil
	}

	// chain
	items, err := parseItems(args["chain"].([]any))
	if err != nil {
		return models.ToolExecutionResult{}, err
	}
	out, err := t.runner.RunChain(ctx, items)
	if err != nil {
		return models.ToolExecutionResult{}, fmt.Errorf("subagent: chain: %w", err)
	}
	return models.NewToolExecutionResultText(out), nil
}

func parseItems(items []any) ([]subagent.TaskItem, error) {
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

func formatParallelResults(results []subagent.Result) string {
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

// Compile-time assertions.
var (
	_ extension.Extension = (*subagentExtension)(nil)
	_ tools.Executable    = (*subagentTool)(nil)
)
