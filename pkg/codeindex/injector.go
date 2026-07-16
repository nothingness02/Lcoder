package codeindex

import (
	"context"
	"fmt"
	"strings"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/models"
)

// Injector writes repo-index stubs into the context manager as a BlockRetrieval block.
type Injector struct {
	indexer   Indexer
	builder   *ContextBuilder
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
	inj := &Injector{
		indexer:   idx,
		manager:   mgr,
		root:      root,
		maxTokens: maxTokens,
	}
	if gs, ok := idx.(GraphStore); ok {
		inj.builder = NewContextBuilder(gs)
	}
	return inj
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

	phrase, keywords := ParseQuery(query)
	q := Query{
		Phrase:     phrase,
		Keywords:   keywords,
		MaxResults: maxResults,
	}

	var results []Result
	var err error
	if inj.builder != nil {
		results, err = inj.builder.Build(ctx, query, BuildOptions{
			MaxSeeds: maxResults,
			MaxDepth: 1,
			MaxNodes: maxResults,
		})
	} else {
		results, err = inj.indexer.Search(ctx, q)
	}
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
	var msgs []models.AgentMessage
	if text != "" {
		text = fmt.Sprintf("// Repository code index results for query %q\n\n%s", query, text)
		msgs = append(msgs, models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: text}))
	}
	block := contextmgr.NewBlockWithCacheHint(
		contextmgr.BlockRetrieval,
		"repo_index",
		contextmgr.StabilityDynamic,
		50,
		contextmgr.CacheHintSkip,
		msgs...,
	)
	inj.manager.SetBlock(block)
	return nil
}
