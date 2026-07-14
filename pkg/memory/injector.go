package memory

import (
	"context"
	"errors"
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
	providers []Provider
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

// WithManager returns a new Injector bound to the supplied context manager,
// preserving the store, ranker, token budget, and providers. Used when an
// agent mode switch clones the context manager.
func (inj *Injector) WithManager(mgr *contextmgr.Manager) *Injector {
	if inj == nil || mgr == nil {
		return nil
	}
	return &Injector{
		store:     inj.store,
		manager:   mgr,
		ranker:    inj.ranker,
		maxTokens: inj.maxTokens,
		providers: inj.providers,
	}
}

// Prefetch ranks memory entries against query and writes a memory_recall block.
func (inj *Injector) Prefetch(ctx context.Context, query string) error {
	entries, err := inj.store.allEntries(MemoryTarget)
	if err != nil {
		return fmt.Errorf("load memory entries: %w", err)
	}

	var failureMsgs []string
	for _, p := range inj.providers {
		if !p.Healthy(ctx) {
			failureMsgs = append(failureMsgs, "external memory provider unavailable")
			continue
		}
		results, err := p.Prefetch(ctx, query)
		if err != nil {
			failureMsgs = append(failureMsgs, err.Error())
			continue
		}
		entries = append(entries, results...)
	}

	ranked := inj.ranker.Rank(query, entries)
	prefix := fmt.Sprintf("// Recalled memory for query %q\n\n", query)
	failureSuffix := ""
	if len(failureMsgs) > 0 {
		failureSuffix = "// External memory provider unavailable: " + strings.Join(failureMsgs, "; ")
	}
	selected := inj.budgetResults(ranked, prefix+failureSuffix)

	inj.setBlock(query, strings.Join(selected, "\n\n"), failureSuffix)
	return nil
}

// SyncTurn forwards a completed user/assistant turn to all healthy providers,
// returning any errors encountered.
func (inj *Injector) SyncTurn(ctx context.Context, user, assistant string) error {
	var errs []error
	for _, p := range inj.providers {
		if !p.Healthy(ctx) {
			continue
		}
		if err := p.SyncTurn(ctx, user, assistant); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// OnSessionEnd forwards the session summary to all healthy providers, returning
// any errors encountered.
func (inj *Injector) OnSessionEnd(ctx context.Context, summary SessionSummary) error {
	var errs []error
	for _, p := range inj.providers {
		if !p.Healthy(ctx) {
			continue
		}
		if err := p.OnSessionEnd(ctx, summary); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (inj *Injector) budgetResults(ranked []RankedEntry, overhead string) []string {
	if len(ranked) == 0 {
		return nil
	}
	estimator := inj.manager.Estimator()
	overheadCost := estimator([]models.AgentMessage{
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: overhead}),
	})
	budget := inj.maxTokens - overheadCost
	if budget < 0 {
		budget = 0
	}
	used := 0
	var selected []string
	for _, r := range ranked {
		msg := models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: r.Text})
		cost := estimator([]models.AgentMessage{msg})
		if used+cost > budget {
			break
		}
		selected = append(selected, r.Text)
		used += cost
	}
	return selected
}

func (inj *Injector) setBlock(query, text, failureSuffix string) {
	if text != "" {
		text = fmt.Sprintf("// Recalled memory for query %q\n\n%s", query, text)
	}
	if failureSuffix != "" {
		if text != "" {
			text = text + "\n\n" + failureSuffix
		} else {
			text = failureSuffix
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
