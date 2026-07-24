package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/tools"
)

// Find searches for files by name pattern.
type Find struct {
	cwd string
}

// NewFind creates a find tool.
func NewFind(cwd string) tools.Executable {
	return &Find{cwd: cwd}
}

func (f *Find) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name:        "find",
		Description: "Find files by name pattern under a directory.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Glob pattern to match file names, e.g. '*.go'",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Directory to search (default cwd)",
				},
			},
			"required": []string{"pattern"},
		},
		ExecutionMode: models.ExecutionParallel,
	}
}

func (f *Find) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	pattern, err := tools.RequiredString(args, "pattern")
	if err != nil {
		return models.ToolExecutionResult{}, err
	}

	path := f.cwd
	if v, ok := args["path"].(string); ok && v != "" {
		path = v
	}
	path = resolveInCwd(f.cwd, path)

	if _, err := filepath.Match(pattern, ""); err != nil {
		return models.ToolExecutionResult{}, fmt.Errorf("invalid glob pattern %q: %v", pattern, err)
	}

	var matches []string
	var walkErrs walkErrorLog
	err = filepath.WalkDir(path, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			walkErrs.record(p, walkErr)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		matched, _ := filepath.Match(pattern, filepath.Base(p))
		if matched {
			rel, _ := filepath.Rel(f.cwd, p)
			matches = append(matches, rel)
			if len(matches) >= maxFindMatches {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		return models.ToolExecutionResult{}, err
	}

	text := strings.Join(matches, "\n")
	if len(matches) >= maxFindMatches {
		text += fmt.Sprintf("\n\n[truncated: %d matches shown; refine pattern or path]", maxFindMatches)
	}
	text += walkErrs.notice()

	return models.ToolExecutionResult{
		Content: []models.ContentPart{models.TextContent{Text: text}},
		Details: map[string]any{"path": path, "matches": len(matches)},
	}, nil
}

var _ tools.Executable = (*Find)(nil)
