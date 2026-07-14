package memory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/stretchr/testify/require"
)

func TestInjectorWritesRecallBlock(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	store, err := NewStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, store.Add(MemoryTarget, "Use Go 1.25 for this project"))
	require.NoError(t, store.Add(MemoryTarget, "Prefer kubernetes for deployment"))
	require.NoError(t, store.Add(MemoryTarget, "Never hardcode secrets"))

	inj := NewInjector(store, mgr, 1024)
	require.NoError(t, inj.Prefetch(context.Background(), "kubernetes deployment"))

	block, ok := mgr.GetBlock(contextmgr.BlockRetrieval, "memory_recall")
	require.True(t, ok)
	text := block.Text()
	require.Contains(t, text, "kubernetes")
	require.NotContains(t, text, "hardcode")
}

func TestInjectorBudgetsTokens(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	store, err := NewStore(t.TempDir())
	require.NoError(t, err)
	// One short entry fits under the 50-token budget; the long entries do not.
	require.NoError(t, store.Add(MemoryTarget, "short relevant note"))
	longEntry := "UNIQUELONG " + strings.Repeat("word ", 80) // ~350 chars, >50 tokens
	for range 4 {
		require.NoError(t, store.Add(MemoryTarget, longEntry))
	}

	inj := NewInjector(store, mgr, 50)
	require.NoError(t, inj.Prefetch(context.Background(), "relevant"))

	block, ok := mgr.GetBlock(contextmgr.BlockRetrieval, "memory_recall")
	require.True(t, ok)
	text := block.Text()
	require.Equal(t, 1, strings.Count(text, "short relevant note"))
	require.Zero(t, strings.Count(text, "UNIQUELONG"))
}

func TestInjectorBudgetSkipsOversizedFirstResult(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	store, err := NewStore(t.TempDir())
	require.NoError(t, err)
	longEntry := "UNIQUEMARKER " + strings.Repeat("word ", 80) // ~350 chars, >50 tokens
	for range 3 {
		require.NoError(t, store.Add(MemoryTarget, longEntry))
	}

	inj := NewInjector(store, mgr, 50)
	require.NoError(t, inj.Prefetch(context.Background(), "UNIQUEMARKER"))

	block, ok := mgr.GetBlock(contextmgr.BlockRetrieval, "memory_recall")
	require.True(t, ok)
	require.Empty(t, block.Text())
}

func TestInjectorClearsBlockWhenNoMatch(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	store, err := NewStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, store.Add(MemoryTarget, "unrelated fact"))

	inj := NewInjector(store, mgr, 1024)
	require.NoError(t, inj.Prefetch(context.Background(), "graphql"))

	block, ok := mgr.GetBlock(contextmgr.BlockRetrieval, "memory_recall")
	require.True(t, ok)
	require.Empty(t, block.Text())
}

func TestInjectorDefaultMaxTokens(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	store, err := NewStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, store.Add(MemoryTarget, "relevant memory"))

	inj := NewInjector(store, mgr, 0)
	require.NoError(t, inj.Prefetch(context.Background(), "relevant"))

	block, ok := mgr.GetBlock(contextmgr.BlockRetrieval, "memory_recall")
	require.True(t, ok)
	require.Contains(t, block.Text(), "relevant memory")
}

type fakeRanker struct {
	results []RankedEntry
}

func (f *fakeRanker) Score(query, entry string) float64 { return 0 }
func (f *fakeRanker) Rank(query string, entries []string) []RankedEntry {
	return f.results
}

func TestInjectorWithRanker(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	store, err := NewStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, store.Add(MemoryTarget, "alpha"))
	require.NoError(t, store.Add(MemoryTarget, "beta"))

	inj := NewInjector(store, mgr, 1024).WithRanker(&fakeRanker{results: []RankedEntry{{Text: "beta", Score: 1.0}}})
	require.NoError(t, inj.Prefetch(context.Background(), "anything"))

	block, ok := mgr.GetBlock(contextmgr.BlockRetrieval, "memory_recall")
	require.True(t, ok)
	require.Contains(t, block.Text(), "beta")
	require.NotContains(t, block.Text(), "alpha")
}

func TestInjectorEmptyQueryClearsBlock(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	store, err := NewStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, store.Add(MemoryTarget, "some memory"))

	inj := NewInjector(store, mgr, 1024)
	require.NoError(t, inj.Prefetch(context.Background(), ""))

	block, ok := mgr.GetBlock(contextmgr.BlockRetrieval, "memory_recall")
	require.True(t, ok)
	require.Empty(t, block.Text())
}

type fakeProvider struct {
	healthy       bool
	prefetched    []string
	prefetchErr   error
	syncTurns     []struct{ user, assistant string }
	sessionEnds   []SessionSummary
	syncTurnErr   error
	sessionEndErr error
}

func (f *fakeProvider) Prefetch(ctx context.Context, query string) ([]string, error) {
	if f.prefetchErr != nil {
		return nil, f.prefetchErr
	}
	return f.prefetched, nil
}

func (f *fakeProvider) SyncTurn(ctx context.Context, user, assistant string) error {
	f.syncTurns = append(f.syncTurns, struct{ user, assistant string }{user, assistant})
	return f.syncTurnErr
}

func (f *fakeProvider) OnSessionEnd(ctx context.Context, summary SessionSummary) error {
	f.sessionEnds = append(f.sessionEnds, summary)
	return f.sessionEndErr
}

func (f *fakeProvider) Healthy(ctx context.Context) bool { return f.healthy }

func TestInjectorAggregatesExternalProvider(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	store, err := NewStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, store.Add(MemoryTarget, "local deployment rule"))

	provider := &fakeProvider{
		healthy:    true,
		prefetched: []string{"external scaling guidance"},
	}

	inj := NewInjector(store, mgr, 1024).
		WithProviders(provider).
		WithRanker(&fakeRanker{results: []RankedEntry{
			{Text: "local deployment rule", Score: 1.0},
			{Text: "external scaling guidance", Score: 0.9},
		}})
	require.NoError(t, inj.Prefetch(context.Background(), "deployment"))

	block, ok := mgr.GetBlock(contextmgr.BlockRetrieval, "memory_recall")
	require.True(t, ok)
	text := block.Text()
	require.Contains(t, text, "local deployment rule")
	require.Contains(t, text, "external scaling guidance")
}

func TestInjectorAppendsProviderErrorToBlock(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	store, err := NewStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, store.Add(MemoryTarget, "local memory still available"))

	provider := &fakeProvider{
		healthy:     true,
		prefetchErr: errors.New("provider connection refused"),
	}

	inj := NewInjector(store, mgr, 1024).
		WithProviders(provider).
		WithRanker(&fakeRanker{results: []RankedEntry{
			{Text: "local memory still available", Score: 1.0},
		}})
	require.NoError(t, inj.Prefetch(context.Background(), "memory"))

	block, ok := mgr.GetBlock(contextmgr.BlockRetrieval, "memory_recall")
	require.True(t, ok)
	text := block.Text()
	require.Contains(t, text, "local memory still available")
	require.Contains(t, text, "External memory provider unavailable: provider connection refused")
}

func TestInjectorSyncTurnAndSessionEnd(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	store, err := NewStore(t.TempDir())
	require.NoError(t, err)

	provider := &fakeProvider{healthy: true}
	inj := NewInjector(store, mgr, 1024).WithProviders(provider)

	require.NoError(t, inj.SyncTurn(context.Background(), "hello", "hi there"))
	require.Len(t, provider.syncTurns, 1)
	require.Equal(t, "hello", provider.syncTurns[0].user)
	require.Equal(t, "hi there", provider.syncTurns[0].assistant)

	summary := SessionSummary{SessionID: "sess-1", TurnCount: 3}
	require.NoError(t, inj.OnSessionEnd(context.Background(), summary))
	require.Len(t, provider.sessionEnds, 1)
	require.Equal(t, summary, provider.sessionEnds[0])
}

func TestInjectorUnhealthyProviderAppendsSuffix(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	store, err := NewStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, store.Add(MemoryTarget, "local memory still available"))

	provider := &fakeProvider{healthy: false}

	inj := NewInjector(store, mgr, 1024).
		WithProviders(provider).
		WithRanker(&fakeRanker{results: []RankedEntry{
			{Text: "local memory still available", Score: 1.0},
		}})
	require.NoError(t, inj.Prefetch(context.Background(), "memory"))

	block, ok := mgr.GetBlock(contextmgr.BlockRetrieval, "memory_recall")
	require.True(t, ok)
	text := block.Text()
	require.Contains(t, text, "local memory still available")
	require.Contains(t, text, "External memory provider unavailable: external memory provider unavailable")
}

func TestInjectorSyncTurnAndSessionEndReturnErrors(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	store, err := NewStore(t.TempDir())
	require.NoError(t, err)

	provider := &fakeProvider{
		healthy:       true,
		syncTurnErr:   errors.New("sync failed"),
		sessionEndErr: errors.New("session end failed"),
	}
	inj := NewInjector(store, mgr, 1024).WithProviders(provider)

	require.ErrorContains(t, inj.SyncTurn(context.Background(), "hello", "hi there"), "sync failed")
	require.ErrorContains(t, inj.OnSessionEnd(context.Background(), SessionSummary{SessionID: "sess-1"}), "session end failed")
}

func TestInjectorMultipleProvidersAllCalled(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	store, err := NewStore(t.TempDir())
	require.NoError(t, err)

	providerA := &fakeProvider{healthy: true}
	providerB := &fakeProvider{healthy: true}
	inj := NewInjector(store, mgr, 1024).WithProviders(providerA, providerB)

	require.NoError(t, inj.SyncTurn(context.Background(), "user", "assistant"))
	require.Len(t, providerA.syncTurns, 1)
	require.Len(t, providerB.syncTurns, 1)

	summary := SessionSummary{SessionID: "sess-2"}
	require.NoError(t, inj.OnSessionEnd(context.Background(), summary))
	require.Len(t, providerA.sessionEnds, 1)
	require.Len(t, providerB.sessionEnds, 1)
}

func TestInjectorWithManagerRebindsContextManager(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	other := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	store, err := NewStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, store.Add(MemoryTarget, "relevant"))

	inj := NewInjector(store, mgr, 1024).WithManager(other)
	require.NoError(t, inj.Prefetch(context.Background(), "relevant"))

	_, ok := mgr.GetBlock(contextmgr.BlockRetrieval, "memory_recall")
	require.False(t, ok, "original manager should not contain the block")
	block, ok := other.GetBlock(contextmgr.BlockRetrieval, "memory_recall")
	require.True(t, ok, "rebound manager should contain the block")
	require.Contains(t, block.Text(), "relevant")
}

func TestInjectorWithManagerPreservesProviders(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	other := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	store, err := NewStore(t.TempDir())
	require.NoError(t, err)

	provider := &fakeProvider{healthy: true}
	inj := NewInjector(store, mgr, 1024).WithProviders(provider).WithManager(other)

	require.NoError(t, inj.SyncTurn(context.Background(), "user", "assistant"))
	require.Len(t, provider.syncTurns, 1)
}
