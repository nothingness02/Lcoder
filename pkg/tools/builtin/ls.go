package builtin

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/tools"
)

// Ls lists directory contents.
type Ls struct {
	cwd string
}


// NewLs creates an ls tool.
func NewLs(cwd string) tools.Executable {
	return &Ls{cwd: cwd}
}

func (l *Ls) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name:        "ls",
		Description: "List files and directories. Defaults to the current working directory.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Directory path to list",
				},
			},
		},
		ExecutionMode: models.ExecutionParallel,
	}
}

// DeclareAccesses implements tools.AccessDeclarer: listing a directory is
// affected by writes anywhere below it, so the read is declared recursive.
func (l *Ls) DeclareAccesses(args map[string]any) []tools.ToolAccess {
	path := l.cwd
	if v, ok := args["path"].(string); ok && v != "" {
		path = v
	}
	return []tools.ToolAccess{{Op: tools.OpRead, Path: resolveInCwd(l.cwd, path), Recursive: true}}
}

func (l *Ls) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	path := l.cwd
	if v, ok := args["path"].(string); ok && v != "" {
		path = v
	}
	path = resolveInCwd(l.cwd, path)

	entries, err := os.ReadDir(path)
	if err != nil {
		return models.ToolExecutionResult{}, err
	}

	var lines []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		lines = append(lines, name)
	}
	sort.Strings(lines)

	truncated := false
	if len(lines) > maxLsEntries {
		lines = lines[:maxLsEntries]
		truncated = true
	}

	text := strings.Join(lines, "\n")
	if truncated {
		text += fmt.Sprintf("\n\n[truncated: %d of %d entries shown; use a more specific path]", maxLsEntries, len(entries))
	}

	return models.ToolExecutionResult{
		Content: []models.ContentPart{models.TextContent{Text: text}},
		Details: map[string]any{"path": path, "count": len(lines)},
	}, nil
}

var _ tools.Executable = (*Ls)(nil)
