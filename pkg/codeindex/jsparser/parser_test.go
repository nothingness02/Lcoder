package jsparser

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lcoder/lcoder/pkg/codeindex"
	"github.com/stretchr/testify/require"
)

func TestJSIndexerExtractsClassesAndMethods(t *testing.T) {
	root := t.TempDir()
	js := filepath.Join(root, "widget.js")
	require.NoError(t, os.WriteFile(js, []byte(`// Base widget.
class Widget extends Component {
  constructor(name) {
    this.name = name;
  }

  // Render the widget.
  render() {
    return null;
  }

  async refresh() {
    await this.load();
  }
}

// Helper function.
function helper() {
  return 1;
}

export async function fetchData(url) {
  return fetch(url);
}
`), 0o644))

	idx := NewIndexer(nil)
	require.NoError(t, idx.Update(context.Background(), root))

	res, err := idx.Search(context.Background(), codeindex.Query{})
	require.NoError(t, err)
	require.Len(t, res, 6)

	names := make(map[string]codeindex.SymbolKind)
	for _, r := range res {
		names[r.Symbol.Name] = r.Symbol.Kind
	}
	require.Contains(t, names, "Widget")
	require.Equal(t, codeindex.SymbolKindType, names["Widget"])
	require.Contains(t, names, "Widget.constructor")
	require.Equal(t, codeindex.SymbolKindMethod, names["Widget.constructor"])
	require.Contains(t, names, "Widget.render")
	require.Equal(t, codeindex.SymbolKindMethod, names["Widget.render"])
	require.Contains(t, names, "Widget.refresh")
	require.Equal(t, codeindex.SymbolKindMethod, names["Widget.refresh"])
	require.Contains(t, names, "helper")
	require.Equal(t, codeindex.SymbolKindFunc, names["helper"])
	require.Contains(t, names, "fetchData")
	require.Equal(t, codeindex.SymbolKindFunc, names["fetchData"])

	var render *codeindex.Result
	for i := range res {
		if res[i].Symbol.Name == "Widget.render" {
			render = &res[i]
			break
		}
	}
	require.NotNil(t, render)
	require.Equal(t, "render()", render.Symbol.Signature)
	require.Contains(t, render.Symbol.Doc, "Render the widget")
}

func TestTSIndexerExtractsClasses(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "api.ts"), []byte(`class API {
  get(url: string): Promise<Response> {}
}
`), 0o644))

	idx := NewIndexer(nil)
	require.NoError(t, idx.Update(context.Background(), root))

	res, err := idx.Search(context.Background(), codeindex.Query{Keywords: []string{"api"}})
	require.NoError(t, err)
	require.Len(t, res, 2)
}

func TestJSIndexerRespectsExcludes(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "keep.js"), []byte("function kept() {}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "skip.js"), []byte("function skipped() {}\n"), 0o644))

	idx := NewIndexer([]string{"skip.js"})
	require.NoError(t, idx.Update(context.Background(), root))

	res, err := idx.Search(context.Background(), codeindex.Query{})
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.Equal(t, "kept", res[0].Symbol.Name)
}
