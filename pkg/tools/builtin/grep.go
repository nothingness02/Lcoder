package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/tools"
)

// Grep searches file contents for a pattern.
type Grep struct {
	cwd string
}

// NewGrep creates a grep tool.
func NewGrep(cwd string) tools.Executable {
	return &Grep{cwd: cwd}
}

func (g *Grep) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name:        "grep",
		Description: "Search file contents for a regular expression under a directory.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Regular expression to search for (Go regexp syntax)",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Directory or file to search (default cwd)",
				},
				"glob": map[string]any{
					"type":        "string",
					"description": "Glob pattern to filter files, e.g. '*.go'",
				},
			},
			"required": []string{"pattern"},
		},
		ExecutionMode: models.ExecutionParallel,
	}
}

func (g *Grep) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	pattern, err := tools.RequiredString(args, "pattern")
	if err != nil {
		return models.ToolExecutionResult{}, err
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return models.ToolExecutionResult{}, fmt.Errorf("invalid regex pattern %q: %v", pattern, err)
	}

	path := g.cwd
	if v, ok := args["path"].(string); ok && v != "" {
		path = v
	}
	path = resolveInCwd(g.cwd, path)

	var glob string
	if v, ok := args["glob"].(string); ok {
		glob = v
	}
	if glob != "" {
		if _, gerr := filepath.Match(glob, ""); gerr != nil {
			return models.ToolExecutionResult{}, fmt.Errorf("invalid glob pattern %q: %v", glob, gerr)
		}
	}

	var matches []string
	var skippedLarge int
	var walkErrs walkErrorLog
	err = filepath.WalkDir(path, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			walkErrs.record(p, walkErr)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if glob != "" {
			matched, _ := filepath.Match(glob, filepath.Base(p))
			if !matched {
				return nil
			}
		}
		info, statErr := d.Info()
		if statErr == nil && info.Size() > maxGrepFileSizeBytes {
			skippedLarge++
			return nil
		}
		data, readErr := os.ReadFile(p)
		if readErr != nil {
			walkErrs.record(p, readErr)
			return nil
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if re.MatchString(line) {
				rel, _ := filepath.Rel(g.cwd, p)
				matches = append(matches, fmt.Sprintf("%s:%d:%s", rel, i+1, line))
				if len(matches) >= maxGrepMatches {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	if err != nil {
		return models.ToolExecutionResult{}, err
	}

	text := strings.Join(matches, "\n")
	if skippedLarge > 0 {
		text += fmt.Sprintf("\n\n[skipped %d file(s) larger than %d bytes]", skippedLarge, maxGrepFileSizeBytes)
	}
	if len(matches) >= maxGrepMatches {
		text += fmt.Sprintf("\n\n[truncated: %d matches shown; refine pattern or path]", maxGrepMatches)
	}
	text += walkErrs.notice()

	return models.ToolExecutionResult{
		Content: []models.ContentPart{models.TextContent{Text: text}},
		Details: map[string]any{"path": path, "matches": len(matches)},
	}, nil
}

var _ tools.Executable = (*Grep)(nil)
