package codeindex

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/models"
)

// Injector writes repo-index stubs into the context manager as a BlockRetrieval block.
type Injector struct {
	indexer   Indexer
	manager   *contextmgr.Manager
	root      string
	maxTokens int
	updated   bool
}

// NewInjector creates an injector bound to a manager and project root.
func NewInjector(idx Indexer, mgr *contextmgr.Manager, root string, maxTokens int) *Injector {
	if maxTokens <= 0 {
		maxTokens = 8192
	}
	return &Injector{
		indexer:   idx,
		manager:   mgr,
		root:      root,
		maxTokens: maxTokens,
	}
}

// Inject searches the index for query and writes matching stubs into context.
func (inj *Injector) Inject(ctx context.Context, query string, maxResults int) error {
	if !inj.updated {
		if err := inj.indexer.Update(ctx, inj.root); err != nil {
			return fmt.Errorf("update code index: %w", err)
		}
		inj.updated = true
	}
	if maxResults <= 0 {
		maxResults = 10
	}
	results, err := inj.indexer.Search(ctx, Query{
		Keywords:   splitQuery(query),
		MaxResults: maxResults,
	})
	if err != nil {
		return err
	}

	var stubs []string
	used := 0
	estimator := inj.manager.Estimator()
	for _, r := range results {
		msg := models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: r.Stub})
		cost := estimator([]models.AgentMessage{msg})
		if used+cost > inj.maxTokens && len(stubs) > 0 {
			break
		}
		stubs = append(stubs, r.Stub)
		used += cost
	}

	text := strings.Join(stubs, "\n\n")
	if text != "" {
		text = fmt.Sprintf("// Repository code index results for query %q\n\n%s", query, text)
	}
	block := contextmgr.NewBlockWithCacheHint(
		contextmgr.BlockRetrieval,
		"repo_index",
		contextmgr.StabilityDynamic,
		50,
		contextmgr.CacheHintSkip,
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: text}),
	)
	inj.manager.SetBlock(block)
	return nil
}

func splitQuery(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || r == '.' || r == '/' || r == '_' || r == '-'
	}) {
		if f != "" {
			out = append(out, strings.ToLower(f))
		}
	}
	return out
}
