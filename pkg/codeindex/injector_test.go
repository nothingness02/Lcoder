package codeindex

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/stretchr/testify/require"
)

type fakeIndexer struct {
	results []Result
}

func (f *fakeIndexer) Update(ctx context.Context, root string) error {
	return nil
}

func (f *fakeIndexer) Search(ctx context.Context, q Query) ([]Result, error) {
	return f.results, nil
}

func (f *fakeIndexer) Clear() error {
	return nil
}

func TestInjectorWritesBlock(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	idx := &fakeIndexer{
		results: []Result{
			{Symbol: Symbol{Name: "Engine"}, Stub: "// demo.go:1\ntype Engine struct{}"},
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
