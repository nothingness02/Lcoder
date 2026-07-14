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
	store       *Store
	manager     *contextmgr.Manager
	ranker      Ranker
	maxTokens   int
	providers   []Provider
	failureMsg  string
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

// WithProviders attaches external memory providers whose results are merged
// with local store entries during Prefetch.
func (inj *Injector) WithProviders(providers ...Provider) *Injector {
	inj.providers = providers
	return inj
}

// Prefetch ranks memory entries against query and writes a memory_recall block.
func (inj *Injector) Prefetch(ctx context.Context, query string) error {
	inj.failureMsg = ""

	entries, err := inj.store.allEntries(MemoryTarget)
	if err != nil {
		inj.setBlock(query, "")
		return fmt.Errorf("load memory entries: %w", err)
	}

	for _, p := range inj.providers {
		if !p.Healthy(ctx) {
			inj.failureMsg = "external memory provider circuit open"
			continue
		}
		results, err := p.Prefetch(ctx, query)
		if err != nil {
			inj.failureMsg = err.Error()
			continue
		}
		entries = append(entries, results...)
	}

	ranked := inj.ranker.Rank(query, entries)
	selected := inj.budgetResults(ranked)

	inj.setBlock(query, strings.Join(selected, "\n\n"))
	return nil
}

// SyncTurn forwards a completed user/assistant turn to all healthy providers,
// returning the first error encountered.
func (inj *Injector) SyncTurn(ctx context.Context, user, assistant string) error {
	for _, p := range inj.providers {
		if !p.Healthy(ctx) {
			continue
		}
		if err := p.SyncTurn(ctx, user, assistant); err != nil {
			return err
		}
	}
	return nil
}

// OnSessionEnd forwards the session summary to all healthy providers, returning
// the first error encountered.
func (inj *Injector) OnSessionEnd(ctx context.Context, summary SessionSummary) error {
	for _, p := range inj.providers {
		if !p.Healthy(ctx) {
			continue
		}
		if err := p.OnSessionEnd(ctx, summary); err != nil {
			return err
		}
	}
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

func (inj *Injector) setBlock(query, text string) {
	if text != "" {
		text = fmt.Sprintf("// Recalled memory for query %q\n\n%s", query, text)
	}
	if inj.failureMsg != "" {
		suffix := fmt.Sprintf("// External memory provider unavailable: %s", inj.failureMsg)
		if text != "" {
			text = text + "\n\n" + suffix
		} else {
			text = suffix
		}
	}

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
