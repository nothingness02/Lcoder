package pythonparser

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lcoder/lcoder/pkg/codeindex"
	"github.com/stretchr/testify/require"
)

func TestPythonIndexerExtractsClassesAndMethods(t *testing.T) {
	root := t.TempDir()
	py := filepath.Join(root, "calculator.py")
	require.NoError(t, os.WriteFile(py, []byte(`class Calculator:
    """Performs basic arithmetic."""

    def add(self, a, b):
        """Add two numbers."""
        return a + b

    async def compute(self, x):
        """Run computation."""
        pass

def helper():
    pass
`), 0o644))

	idx := NewIndexer(nil)
	require.NoError(t, idx.Update(context.Background(), root))

	res, err := idx.Search(context.Background(), codeindex.Query{})
	require.NoError(t, err)
	require.Len(t, res, 4)

	names := make(map[string]codeindex.SymbolKind)
	for _, r := range res {
		names[r.Symbol.Name] = r.Symbol.Kind
	}
	require.Contains(t, names, "Calculator")
	require.Equal(t, codeindex.SymbolKindType, names["Calculator"])
	require.Contains(t, names, "Calculator.add")
	require.Equal(t, codeindex.SymbolKindMethod, names["Calculator.add"])
	require.Contains(t, names, "Calculator.compute")
	require.Equal(t, codeindex.SymbolKindMethod, names["Calculator.compute"])
	require.Contains(t, names, "helper")
	require.Equal(t, codeindex.SymbolKindFunc, names["helper"])

	var calc *codeindex.Result
	for i := range res {
		if res[i].Symbol.Name == "Calculator" {
			calc = &res[i]
			break
		}
	}
	require.NotNil(t, calc)
	require.Contains(t, calc.Stub, "class Calculator")
	require.Contains(t, calc.Symbol.Doc, "Performs basic arithmetic")

	var add *codeindex.Result
	for i := range res {
		if res[i].Symbol.Name == "Calculator.add" {
			add = &res[i]
			break
		}
	}
	require.NotNil(t, add)
	require.Equal(t, "def add(self, a, b)", add.Symbol.Signature)
}

func TestPythonIndexerRespectsExcludes(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "keep.py"), []byte("def kept(): pass\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "skip.py"), []byte("def skipped(): pass\n"), 0o644))

	idx := NewIndexer([]string{"skip.py"})
	require.NoError(t, idx.Update(context.Background(), root))

	res, err := idx.Search(context.Background(), codeindex.Query{Keywords: []string{"def"}})
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.Equal(t, "kept", res[0].Symbol.Name)
}
