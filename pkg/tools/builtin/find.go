package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/sandbox"
	"github.com/lcoder/lcoder/pkg/tools"
)

// Find searches for files by name pattern.
type Find struct {
	cwd string
	sb  sandbox.Sandbox
}

// UseSandbox injects the sandbox used to enforce filesystem checks.
func (f *Find) UseSandbox(sb sandbox.Sandbox) { f.sb = sb }

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
	pattern := args["pattern"].(string)

	path := f.cwd
	if v, ok := args["path"].(string); ok && v != "" {
		path = v
	}
	path, err := resolveAndCheck(f.cwd, f.sb, path, sandbox.FSRead)
	if err != nil {
		return models.ToolExecutionResult{}, err
	}

	var matches []string
	err = filepath.WalkDir(path, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if f.sb != nil {
			if cerr := f.sb.Filesystem().Check(p, sandbox.FSRead); cerr != nil {
				return nil // skip out-of-bounds child
			}
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

	return models.ToolExecutionResult{
		Content: []models.ContentPart{models.TextContent{Text: text}},
		Details: map[string]any{"path": path, "matches": len(matches)},
	}, nil
}

var _ tools.Executable = (*Find)(nil)
