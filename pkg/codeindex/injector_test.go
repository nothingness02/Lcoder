package codeindex

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/stretchr/testify/require"
)

type fakeIndexer struct {
	results []Result
	queries []Query
}

func (f *fakeIndexer) Update(ctx context.Context, root string) error {
	return nil
}

func (f *fakeIndexer) Search(ctx context.Context, q Query) ([]Result, error) {
	f.queries = append(f.queries, q)
	return f.results, nil
}

func (f *fakeIndexer) Clear() error {
	return nil
}

func TestInjectorWritesBlock(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	idx := &fakeIndexer{
		results: []Result{
			{Node: Node{Name: "Engine"}, Stub: "// demo.go:1\ntype Engine struct{}"},
		},
	}
	inj := NewInjector(idx, mgr, "/tmp/demo", 2000)

	require.NoError(t, inj.Inject(context.Background(), "engine", 5))

	block, ok := mgr.GetBlock(contextmgr.BlockRetrieval, "repo_index")
	require.True(t, ok)
	text := block.Text()
	require.Contains(t, text, "Engine")
	require.Contains(t, text, "Repository code index results")
}

func TestInjectorEmptyResultsProduceNoMessages(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	idx := &fakeIndexer{results: nil}
	inj := NewInjector(idx, mgr, "/tmp/demo", 2000)

	require.NoError(t, inj.Inject(context.Background(), "unknown", 5))

	block, ok := mgr.GetBlock(contextmgr.BlockRetrieval, "repo_index")
	require.True(t, ok)
	require.Empty(t, block.Text())
	require.Empty(t, block.Messages, "empty index block must not contain a system message with empty content")
}

func TestInjectorParsesQuery(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	idx := &fakeIndexer{}
	inj := NewInjector(idx, mgr, "/tmp/demo", 2000)

	require.NoError(t, inj.Inject(context.Background(), "RunAgent daemon", 5))

	require.Len(t, idx.queries, 1)
	q := idx.queries[0]
	require.Equal(t, "runagent daemon", q.Phrase)
	seen := make(map[string]bool)
	for _, kw := range q.Keywords {
		seen[kw] = true
	}
	require.True(t, seen["runagent"], "expected expanded identifier keyword")
	require.True(t, seen["run"], "expected camelCase split keyword")
	require.True(t, seen["agent"], "expected camelCase split keyword")
	require.True(t, seen["daemon"], "expected plain keyword")
}
