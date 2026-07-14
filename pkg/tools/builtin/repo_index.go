package builtin

import (
	"context"
	"fmt"

	"github.com/lcoder/lcoder/pkg/codeindex"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/tools"
)

// RepoIndex is a tool that injects repository code stubs into context.
type RepoIndex struct {
	cwd      string
	injector *codeindex.Injector
}

// NewRepoIndex creates the tool. Call SetInjector before Execute.
func NewRepoIndex(cwd string) *RepoIndex {
	return &RepoIndex{cwd: cwd}
}

// SetInjector wires the injector after the context manager is available.
func (r *RepoIndex) SetInjector(inj *codeindex.Injector) {
	r.injector = inj
}

func (r *RepoIndex) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name:        "repo_index",
		Description: "Search the repository code index and inject relevant symbol stubs into the context. Use this as the FIRST step when exploring code structure, locating symbols, or tracing call chains; it is cheaper and more precise than broad grep/ls/read. Supports Go, Python, JavaScript, and TypeScript source files when configured.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Keywords or symbol names to search for",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"description": "Maximum number of stubs to inject (default 10)",
				},
			},
			"required": []string{"query"},
		},
	}
}

func (r *RepoIndex) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	if r.injector == nil {
		return models.ToolExecutionResult{}, fmt.Errorf("repo_index not wired")
	}
	query, _ := args["query"].(string)
	if query == "" {
		return models.ToolExecutionResult{}, fmt.Errorf("missing query")
	}
	maxResults := 10
	if v, ok := args["max_results"].(float64); ok {
		maxResults = int(v)
	} else if v, ok := args["max_results"].(int); ok {
		maxResults = v
	}
	if err := r.injector.Inject(ctx, query, maxResults); err != nil {
		return models.ToolExecutionResult{}, err
	}
	return models.ToolExecutionResult{
		Content: []models.ContentPart{
			models.TextContent{Text: fmt.Sprintf("Repository code index context injected for query %q", query)},
		},
		Details: map[string]any{"query": query, "max_results": maxResults},
	}, nil
}

var _ tools.Executable = (*RepoIndex)(nil)
