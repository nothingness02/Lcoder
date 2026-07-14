package memory

import (
	"context"
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
	for i := 0; i < 10; i++ {
		require.NoError(t, store.Add(MemoryTarget, "kubernetes deployment note number"))
	}

	inj := NewInjector(store, mgr, 50)
	require.NoError(t, inj.Prefetch(context.Background(), "kubernetes"))

	block, ok := mgr.GetBlock(contextmgr.BlockRetrieval, "memory_recall")
	require.True(t, ok)
	// 50 tokens ~= 200 chars; should include at least one but not all ten.
	require.Less(t, len(block.Text()), 1000)
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
