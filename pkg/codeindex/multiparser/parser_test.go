package multiparser

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lcoder/lcoder/pkg/codeindex"
	"github.com/stretchr/testify/require"
)

func TestMultiparserIndexesMixedLanguages(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "app.py"), []byte("class App:\n    def run(self): pass\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "widget.js"), []byte("class Widget {\n  render() {}\n}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "api.ts"), []byte("class API {\n  get() {}\n}\n"), 0o644))

	idx := NewIndexer([]string{"go", "python", "javascript", "typescript"}, []string{"node_modules/"})
	require.NoError(t, idx.Update(context.Background(), root))

	res, err := idx.Search(context.Background(), codeindex.Query{})
	require.NoError(t, err)

	names := make(map[string]bool)
	for _, r := range res {
		names[r.Node.Name] = true
	}
	require.Contains(t, names, "main")
	require.Contains(t, names, "App.run")
	require.Contains(t, names, "Widget.render")
	require.Contains(t, names, "API.get")
}

func TestMultiparserIgnoresUnconfiguredLanguages(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "app.py"), []byte("def run(): pass\n"), 0o644))

	idx := NewIndexer([]string{"go"}, nil)
	require.NoError(t, idx.Update(context.Background(), root))

	res, err := idx.Search(context.Background(), codeindex.Query{})
	require.NoError(t, err)
	require.Empty(t, res)
}
