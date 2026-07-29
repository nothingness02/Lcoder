package hooks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lcoder/lcoder/pkg/agent"
)

// SensitiveFileCheck blocks or warns on read/write access to sensitive paths.
// The raw argument is matched along with its cleaned and cwd-resolved forms,
// so "./x/../.env" or a relative spelling of an absolute protected path
// cannot slip past a pattern.
func SensitiveFileCheck(patterns []string) agent.BeforeToolCallHook {
	return func(ctx context.Context, info agent.ToolCallInfo) (*agent.BeforeToolCallResult, error) {
		if info.ToolCall.Name != "read" && info.ToolCall.Name != "write" && info.ToolCall.Name != "edit" {
			return nil, nil
		}
		pathArg, _ := info.Args["path"].(string)
		if pathArg == "" {
			return nil, nil
		}
		candidates := []string{pathArg, filepath.Clean(pathArg)}
		if !filepath.IsAbs(pathArg) {
			if cwd, err := os.Getwd(); err == nil {
				candidates = append(candidates, filepath.Join(cwd, pathArg))
			}
		}
		for _, candidate := range candidates {
			for _, pattern := range patterns {
				matched, err := matchPattern(pattern, candidate)
				if err != nil {
					return nil, err
				}
				if matched {
					return &agent.BeforeToolCallResult{
						Block:  true,
						Reason: fmt.Sprintf("access to sensitive path blocked: %s matches %q", pathArg, pattern),
					}, nil
				}
			}
		}
		return nil, nil
	}
}

func matchPattern(pattern, path string) (bool, error) {
	if strings.Contains(pattern, "*") {
		// Patterns with a path separator ("secrets/*") match the full path;
		// plain globs ("*.env") match the basename. Matching only the
		// basename would silently disable path-shaped patterns.
		target := filepath.Base(path)
		if strings.ContainsAny(pattern, `/\`) {
			target = filepath.ToSlash(filepath.Clean(path))
		}
		matched, err := filepath.Match(filepath.ToSlash(pattern), target)
		if err != nil {
			return false, err
		}
		return matched, nil
	}
	return strings.Contains(path, pattern), nil
}
