package builtin

import (
	"context"
	"fmt"

	"github.com/lcoder/lcoder/pkg/memory"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/tools"
)

// Memory manages persistent global memory entries.
type Memory struct {
	cwd   string
	store *memory.Store
}

// NewMemory creates the memory tool bound to cwd and the memory store.
func NewMemory(cwd string, store *memory.Store) tools.Executable {
	return &Memory{cwd: cwd, store: store}
}

func (m *Memory) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "memory",
		Description: "Manage persistent global memory across sessions. " +
			"Use this to save user preferences, project conventions, environment facts, or lessons learned. " +
			"The tool operates on the global memory file; project-level memory files are read-only and can be edited manually.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []any{"add", "replace", "remove"},
					"description": "Operation to perform.",
				},
				"target": map[string]any{
					"type":        "string",
					"enum":        []any{"memory", "user"},
					"description": "Which memory channel to modify.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "New entry text for add/replace.",
				},
				"old_text": map[string]any{
					"type":        "string",
					"description": "Short unique substring of the entry to replace or remove.",
				},
			},
			"required": []any{"action", "target"},
		},
		ExecutionMode: models.ExecutionSequential,
	}
}

func (m *Memory) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	action, _ := args["action"].(string)
	targetName, _ := args["target"].(string)
	content, _ := args["content"].(string)
	oldText, _ := args["old_text"].(string)

	var target memory.Target
	switch targetName {
	case "user":
		target = memory.UserTarget
	case "memory":
		target = memory.MemoryTarget
	default:
		return models.ToolExecutionResult{}, fmt.Errorf("target must be 'memory' or 'user'")
	}

	var err error
	switch action {
	case "add":
		if content == "" {
			return models.ToolExecutionResult{}, fmt.Errorf("content is required for add")
		}
		err = m.store.Add(target, content)
	case "replace":
		if oldText == "" {
			return models.ToolExecutionResult{}, fmt.Errorf("old_text is required for replace")
		}
		if content == "" {
			return models.ToolExecutionResult{}, fmt.Errorf("content is required for replace")
		}
		err = m.store.Replace(target, oldText, content)
	case "remove":
		if oldText == "" {
			return models.ToolExecutionResult{}, fmt.Errorf("old_text is required for remove")
		}
		err = m.store.Remove(target, oldText)
	default:
		return models.ToolExecutionResult{}, fmt.Errorf("action must be 'add', 'replace' or 'remove'")
	}
	if err != nil {
		return models.ToolExecutionResult{}, err
	}

	usage, _ := m.store.UsageString(target)
	msg := fmt.Sprintf("Memory updated (%s). Usage: %s.", targetName, usage)
	return models.ToolExecutionResult{
		Content: []models.ContentPart{models.TextContent{Text: msg}},
		Details: map[string]any{"usage": usage, "target": targetName, "action": action},
	}, nil
}

var _ tools.Executable = (*Memory)(nil)
