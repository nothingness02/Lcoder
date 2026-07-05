package builtin

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/sandbox"
	"github.com/lcoder/lcoder/pkg/tools"
)

// Read reads files with optional offset/limit.
type Read struct {
	cwd string
	sb  sandbox.Sandbox
}

// UseSandbox injects the sandbox used to enforce filesystem checks.
func (r *Read) UseSandbox(sb sandbox.Sandbox) { r.sb = sb }

// NewRead creates a read tool.
func NewRead(cwd string) tools.Executable {
	return &Read{cwd: cwd}
}

func (r *Read) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name:        "read",
		Description: "Read the contents of a file. Supports text files and images. For text files, output is truncated; use offset/limit for large files.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file to read (relative or absolute)",
				},
				"offset": map[string]any{
					"type":        "integer",
					"description": "Line number to start from (1-indexed)",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of lines to read",
				},
			},
			"required": []string{"path"},
		},
		ExecutionMode: models.ExecutionParallel,
	}
}

func (r *Read) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	path := args["path"].(string)
	path, err := resolveAndCheck(r.cwd, r.sb, path, sandbox.FSRead)
	if err != nil {
		return models.ToolExecutionResult{}, err
	}

	info, err := os.Stat(path)
	if err != nil {
		return models.ToolExecutionResult{}, err
	}
	if info.IsDir() {
		return models.ToolExecutionResult{}, fmt.Errorf("path is a directory: %s", path)
	}
	if info.Size() > maxReadFileSizeBytes {
		return models.ToolExecutionResult{}, fmt.Errorf(
			"file too large (%d bytes > %d bytes); use offset/limit or read a smaller section",
			info.Size(), maxReadFileSizeBytes)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return models.ToolExecutionResult{}, err
	}

	text := string(data)
	lines := strings.Split(text, "\n")

	offset := 1
	if v, ok := args["offset"].(float64); ok {
		offset = int(v)
	}
	userLimit := 0
	if v, ok := args["limit"].(float64); ok && int(v) > 0 {
		userLimit = int(v)
	}
	limit := userLimit
	if limit == 0 {
		limit = defaultReadLines
	}

	start := offset - 1
	if start < 0 {
		start = 0
	}
	if start > len(lines) {
		start = len(lines)
	}
	end := start + limit
	truncated := userLimit == 0 && end < len(lines)
	if end > len(lines) {
		end = len(lines)
	}

	selected := strings.Join(lines[start:end], "\n")
	if truncated {
		selected += fmt.Sprintf(
			"\n\n[truncated: showing lines %d-%d of %d; use offset/limit to read more]",
			start+1, end, len(lines))
	}
	return models.ToolExecutionResult{
		Content: []models.ContentPart{
			models.TextContent{Text: selected},
		},
		Details: map[string]any{
			"path":  path,
			"lines": fmt.Sprintf("%d-%d", start+1, end),
		},
	}, nil
}

// Ensure Read implements Executable.
var _ tools.Executable = (*Read)(nil)
