package memory

import (
	"context"
	"fmt"
	"strings"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/models"
)

// Injector prefetches relevant memory entries into the context manager each turn.
type Injector struct {
	store     *Store
	manager   *contextmgr.Manager
	ranker    Ranker
	maxTokens int
}

// NewInjector creates an injector bound to a store and context manager.
// maxTokens <= 0 defaults to 1024.
func NewInjector(store *Store, mgr *contextmgr.Manager, maxTokens int) *Injector {
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	return &Injector{
		store:     store,
		manager:   mgr,
		ranker:    NewDefaultRanker(),
		maxTokens: maxTokens,
	}
}

// WithRanker replaces the default ranker. Useful for tests or future providers.
func (inj *Injector) WithRanker(r Ranker) *Injector {
	inj.ranker = r
	return inj
}

// Prefetch ranks memory entries against query and writes a memory_recall block.
func (inj *Injector) Prefetch(ctx context.Context, query string) error {
	entries, err := inj.store.allEntries(MemoryTarget)
	if err != nil {
		inj.setBlock("")
		return fmt.Errorf("load memory entries: %w", err)
	}

	ranked := inj.ranker.Rank(query, entries)
	selected := inj.budgetResults(ranked)

	text := strings.Join(selected, "\n\n")
	if text != "" {
		text = fmt.Sprintf("// Recalled memory for query %q\n\n%s", query, text)
	}
	inj.setBlock(text)
	return nil
}

func (inj *Injector) budgetResults(ranked []RankedEntry) []string {
	if len(ranked) == 0 {
		return nil
	}
	estimator := inj.manager.Estimator()
	used := 0
	var selected []string
	for _, r := range ranked {
		msg := models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: r.Text})
		cost := estimator([]models.AgentMessage{msg})
		if used+cost > inj.maxTokens {
			break
		}
		selected = append(selected, r.Text)
		used += cost
	}
	return selected
}

func (inj *Injector) setBlock(text string) {
	block := contextmgr.NewBlockWithCacheHint(
		contextmgr.BlockRetrieval,
		"memory_recall",
		contextmgr.StabilityDynamic,
		60,
		contextmgr.CacheHintSkip,
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: text}),
	)
	inj.manager.SetBlock(block)
}
