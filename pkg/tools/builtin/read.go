package builtin

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/tools"
)

// Read reads files with optional offset/limit.
type Read struct {
	cwd string
}


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
	path, err := tools.RequiredString(args, "path")
	if err != nil {
		return models.ToolExecutionResult{}, err
	}
	offset := tools.Int(args, "offset", 1)
	userLimit := tools.Int(args, "limit", 0)

	path = resolveInCwd(r.cwd, path)

	info, err := os.Stat(path)
	if err != nil {
		return models.ToolExecutionResult{}, err
	}
	if info.IsDir() {
		return models.ToolExecutionResult{}, fmt.Errorf("path is a directory: %s", path)
	}
	// Large files stay readable through offset/limit windows; only reject when
	// no window was requested, and tell the model exactly how to retry. Beyond
	// the hard cap the file is not read into memory at all.
	if info.Size() > maxReadFileSizeHardBytes {
		return models.ToolExecutionResult{}, fmt.Errorf(
			"file too large (%d bytes > %d bytes hard limit); use bash with head/tail/sed to inspect a section",
			info.Size(), maxReadFileSizeHardBytes)
	}
	if info.Size() > maxReadFileSizeBytes && userLimit <= 0 {
		return models.ToolExecutionResult{}, fmt.Errorf(
			"file too large (%d bytes > %d bytes); pass offset/limit to read a window, e.g. {\"path\": %q, \"offset\": 1, \"limit\": %d}",
			info.Size(), maxReadFileSizeBytes, path, defaultReadLines)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return models.ToolExecutionResult{}, err
	}

	text := string(data)
	lines := strings.Split(text, "\n")

	limit := userLimit
	if limit == 0 {
		limit = defaultReadLines
	}

	start := offset - 1
	if start < 0 {
		start = 0
	}
	if start >= len(lines) {
		return models.ToolExecutionResult{}, fmt.Errorf(
			"offset %d is beyond end of file (%d lines total)", offset, len(lines))
	}
	end := start + limit
	truncated := end < len(lines)
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
