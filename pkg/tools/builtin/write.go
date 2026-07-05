package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/sandbox"
	"github.com/lcoder/lcoder/pkg/tools"
)

// Write writes content to a file.
type Write struct {
	cwd string
	sb  sandbox.Sandbox
}

// UseSandbox injects the sandbox used to enforce filesystem checks.
func (w *Write) UseSandbox(sb sandbox.Sandbox) { w.sb = sb }

// NewWrite creates a write tool.
func NewWrite(cwd string) tools.Executable {
	return &Write{cwd: cwd}
}

func (w *Write) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name:        "write",
		Description: "Write content to a file. Creates parent directories if needed and overwrites existing files.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file to write",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "Content to write",
				},
			},
			"required": []string{"path", "content"},
		},
		ExecutionMode: models.ExecutionParallel,
	}
}

func (w *Write) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	path := args["path"].(string)
	content := args["content"].(string)
	path, err := resolveAndCheck(w.cwd, w.sb, path, sandbox.FSWrite)
	if err != nil {
		return models.ToolExecutionResult{}, err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return models.ToolExecutionResult{}, err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return models.ToolExecutionResult{}, err
	}

	return models.ToolExecutionResult{
		Content: []models.ContentPart{
			models.TextContent{Text: fmt.Sprintf("Wrote %d characters to %s", len(content), path)},
		},
		Details: map[string]any{"path": path},
	}, nil
}

var _ tools.Executable = (*Write)(nil)
