package builtin

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/codeindex"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/stretchr/testify/require"
)

type fakeRepoIndexer struct{}

func (f *fakeRepoIndexer) Update(ctx context.Context, root string) error { return nil }
func (f *fakeRepoIndexer) Search(ctx context.Context, q codeindex.Query) ([]codeindex.Result, error) {
	return []codeindex.Result{{Symbol: codeindex.Symbol{Name: "Finder"}, Stub: "type Finder struct{}"}}, nil
}
func (f *fakeRepoIndexer) Clear() error { return nil }

func TestRepoIndexTool(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	inj := codeindex.NewInjector(&fakeRepoIndexer{}, mgr, "/tmp/demo", 2000)

	tool := NewRepoIndex("/tmp/demo")
	tool.SetInjector(inj)

	res, err := tool.Execute(context.Background(), "call-1", map[string]any{"query": "finder"})
	require.NoError(t, err)
	require.Contains(t, res.Text(), "Go repo context injected")
}

func TestRepoIndexToolMissingQuery(t *testing.T) {
	tool := NewRepoIndex("/tmp/demo")
	tool.SetInjector(&codeindex.Injector{})
	_, err := tool.Execute(context.Background(), "call-1", map[string]any{})
	require.Error(t, err)
}
