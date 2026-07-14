package memory

import (
	"context"
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
