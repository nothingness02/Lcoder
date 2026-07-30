package builtin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/tools"
)

// Find searches for files by name pattern. It shells out to ripgrep when
// available (gitignore-aware, full path glob support, newest-first) and
// falls back to a built-in walk that matches file base names otherwise.
type Find struct {
	cwd string
}

// NewFind creates a find tool.
func NewFind(cwd string) tools.Executable {
	return &Find{cwd: cwd}
}

func (f *Find) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "find",
		Description: "Find files by glob pattern under a directory, most recently modified first. Uses ripgrep when available " +
			"(respects .gitignore; path patterns like 'src/**/*.go' supported); otherwise a built-in fallback matches file base names only.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Glob pattern, e.g. '*.go' (any depth) or 'src/**/*.go' (requires ripgrep)",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Directory to search (default cwd)",
				},
			},
			"required": []string{"pattern"},
		},
	}
}

// DeclareAccesses implements tools.AccessDeclarer: find searches a tree read-only.
func (f *Find) DeclareAccesses(args map[string]any) []tools.ToolAccess {
	path := f.cwd
	if v, ok := args["path"].(string); ok && v != "" {
		path = v
	}
	return []tools.ToolAccess{{Op: tools.OpSearch, Path: resolveInCwd(f.cwd, path), Recursive: true}}
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
	root := resolveInCwd(f.cwd, path)

	var matches []string
	var truncated bool
	var notice string
	if rg := rgBinaryPath(); rg != "" {
		matches, truncated, err = f.executeRipgrep(ctx, rg, root, pattern)
	} else {
		matches, truncated, notice, err = f.executeFallback(root, pattern)
	}
	if err != nil {
		return models.ToolExecutionResult{}, err
	}

	var text string
	if len(matches) == 0 {
		text = "no files matched"
	} else {
		text = strings.Join(matches, "\n")
	}
	if truncated {
		text += fmt.Sprintf("\n\n[truncated: %d matches shown; refine pattern or path]", maxFindMatches)
	}
	text += notice

	return models.ToolExecutionResult{
		Content: []models.ContentPart{models.TextContent{Text: text}},
		Details: map[string]any{"path": root, "matches": len(matches)},
	}, nil
}

func (f *Find) executeRipgrep(ctx context.Context, rg, root, pattern string) ([]string, bool, error) {
	// Pin the child cwd to the search root and pass "." as the path: rg
	// globs match the path form handed to rg, so a pattern containing '/'
	// (e.g. src/**/*.go) would never match against absolute paths.
	cmdArgs := []string{"--files", "--hidden", "--sortr=modified"}
	cmdArgs = append(cmdArgs, vcsExcludeArgs()...)
	cmdArgs = append(cmdArgs, "--glob", pattern, "--", ".")

	stdout, stderr, _, err := runSearchCommand(ctx, root, rg, cmdArgs...)
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return nil, false, nil
		}
		return nil, false, classifySearchError("find", stderr, err)
	}

	var matches []string
	truncated := false
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		line = strings.TrimPrefix(line, "./")
		line = strings.TrimPrefix(line, `.\`)
		if line == "" {
			continue
		}
		matches = append(matches, relDisplayPath(f.cwd, filepath.Join(root, line)))
		if len(matches) >= maxFindMatches {
			truncated = true
			break
		}
	}
	return matches, truncated, nil
}

func (f *Find) executeFallback(root, pattern string) ([]string, bool, string, error) {
	if _, err := filepath.Match(pattern, ""); err != nil {
		return nil, false, "", fmt.Errorf("invalid glob pattern %q: %v", pattern, err)
	}

	var matches []string
	var walkErrs walkErrorLog
	truncated := false
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			walkErrs.record(p, walkErr)
			return nil
		}
		if d.IsDir() {
			if isVCSDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		matched, _ := filepath.Match(pattern, filepath.Base(p))
		if matched {
			matches = append(matches, relDisplayPath(f.cwd, p))
			if len(matches) >= maxFindMatches {
				truncated = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		return nil, false, "", err
	}
	return matches, truncated, walkErrs.notice(), nil
}

var _ tools.Executable = (*Find)(nil)
