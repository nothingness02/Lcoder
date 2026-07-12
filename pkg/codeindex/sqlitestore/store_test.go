package sqlitestore

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/codeindex"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

func TestIndexerUpdateAndSearch(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(t.TempDir(), "index.db")

	require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644))

	idx, err := NewIndexer([]string{"go"}, nil, db)
	require.NoError(t, err)
	defer idx.Close()

	require.NoError(t, idx.Update(context.Background(), root))

	res, err := idx.Search(context.Background(), codeindex.Query{Keywords: []string{"main"}})
	require.NoError(t, err)
	require.NotEmpty(t, res)
	require.Equal(t, "main", res[0].Node.Name)
}

func TestIndexerIncrementalSkipsUnchangedFiles(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(t.TempDir(), "index.db")

	path := filepath.Join(root, "demo.go")
	require.NoError(t, os.WriteFile(path, []byte("package demo\nfunc Foo() {}\n"), 0o644))

	idx, err := NewIndexer([]string{"go"}, nil, db)
	require.NoError(t, err)
	defer idx.Close()

	require.NoError(t, idx.Update(context.Background(), root))
	res, err := idx.Search(context.Background(), codeindex.Query{Keywords: []string{"foo"}})
	require.NoError(t, err)
	require.Len(t, res, 1)

	// Re-index without changes: the file should still be searchable and the
	// parser should not crash on a cleared snapshot.
	require.NoError(t, idx.Update(context.Background(), root))
	res, err = idx.Search(context.Background(), codeindex.Query{Keywords: []string{"foo"}})
	require.NoError(t, err)
	require.Len(t, res, 1)
}

func TestIndexerDetectsChangedFiles(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(t.TempDir(), "index.db")

	path := filepath.Join(root, "demo.go")
	require.NoError(t, os.WriteFile(path, []byte("package demo\nfunc Foo() {}\n"), 0o644))

	idx, err := NewIndexer([]string{"go"}, nil, db)
	require.NoError(t, err)
	defer idx.Close()

	require.NoError(t, idx.Update(context.Background(), root))

	// Modify file after a short sleep to ensure different mod time.
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, os.WriteFile(path, []byte("package demo\nfunc Bar() {}\n"), 0o644))

	require.NoError(t, idx.Update(context.Background(), root))

	res, err := idx.Search(context.Background(), codeindex.Query{Keywords: []string{"bar"}})
	require.NoError(t, err)
	require.Len(t, res, 1)

	res, err = idx.Search(context.Background(), codeindex.Query{Keywords: []string{"foo"}})
	require.NoError(t, err)
	require.Empty(t, res)
}

func TestIndexerRemovesDeletedFiles(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(t.TempDir(), "index.db")

	path := filepath.Join(root, "demo.go")
	require.NoError(t, os.WriteFile(path, []byte("package demo\nfunc Foo() {}\n"), 0o644))

	idx, err := NewIndexer([]string{"go"}, nil, db)
	require.NoError(t, err)
	defer idx.Close()

	require.NoError(t, idx.Update(context.Background(), root))
	require.NoError(t, os.Remove(path))
	require.NoError(t, idx.Update(context.Background(), root))

	res, err := idx.Search(context.Background(), codeindex.Query{Keywords: []string{"foo"}})
	require.NoError(t, err)
	require.Empty(t, res)
}

func TestIndexerClear(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(t.TempDir(), "index.db")
	require.NoError(t, os.WriteFile(filepath.Join(root, "demo.go"), []byte("package demo\nfunc Foo() {}\n"), 0o644))

	idx, err := NewIndexer([]string{"go"}, nil, db)
	require.NoError(t, err)
	defer idx.Close()

	require.NoError(t, idx.Update(context.Background(), root))
	require.NoError(t, idx.Clear())

	res, err := idx.Search(context.Background(), codeindex.Query{Keywords: []string{"foo"}})
	require.NoError(t, err)
	require.Empty(t, res)
}

func TestHybridSearchRanked(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(t.TempDir(), "index.db")
	require.NoError(t, os.WriteFile(filepath.Join(root, "demo.go"), []byte(`package demo
// Manager manages things.
type Manager struct{}
func NewManager() *Manager { return &Manager{} }
func (m *Manager) Run() {}
`), 0o644))

	idx, err := NewIndexer([]string{"go"}, nil, db)
	require.NoError(t, err)
	defer idx.Close()
	require.NoError(t, idx.Update(context.Background(), root))

	// Exact symbol match should outrank substring matches.
	res, err := idx.Search(context.Background(), codeindex.Query{
		Symbols:    []string{"Manager"},
		Keywords:   []string{"manager"},
		MaxResults: 5,
	})
	require.NoError(t, err)
	require.NotEmpty(t, res)
	require.Equal(t, "Manager", res[0].Node.Name)

	// NodeByID retrieval.
	n, ok, err := idx.NodeByID(context.Background(), "Manager")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "Manager", n.Name)
}

func TestNeighborsQuery(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(t.TempDir(), "index.db")
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\nfunc A() {}\nfunc B() {}\n"), 0o644))

	idx, err := NewIndexer([]string{"go"}, nil, db)
	require.NoError(t, err)
	defer idx.Close()
	require.NoError(t, idx.Update(context.Background(), root))

	// Insert a synthetic call edge via raw SQL.
	raw, err := sql.Open("sqlite", db+"?_busy_timeout=5000")
	require.NoError(t, err)
	defer raw.Close()
	_, err = raw.Exec("INSERT INTO edges(source, target, kind) VALUES(?, ?, ?)", "A", "B", "calls")
	require.NoError(t, err)

	edges, err := idx.Neighbors(context.Background(), "A", []codeindex.EdgeKind{codeindex.EdgeKindCalls}, "out")
	require.NoError(t, err)
	require.Len(t, edges, 1)
	require.Equal(t, "B", edges[0].Target)
}

func TestResolverStoresUnresolvedRefs(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(t.TempDir(), "index.db")
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"), []byte(`package main
import "fmt"
func main() { fmt.Println("hello") }
`), 0o644))

	idx, err := NewIndexer([]string{"go"}, nil, db)
	require.NoError(t, err)
	defer idx.Close()
	require.NoError(t, idx.Update(context.Background(), root))

	raw, err := sql.Open("sqlite", db+"?_busy_timeout=5000")
	require.NoError(t, err)
	defer raw.Close()

	var count int
	require.NoError(t, raw.QueryRow("SELECT count(*) FROM unresolved_refs WHERE target = ?", "fmt.Println").Scan(&count))
	require.GreaterOrEqual(t, count, 1)
}

func TestDefaultPath(t *testing.T) {
	p := DefaultPath("/tmp/project")
	require.Contains(t, p, ".lcoder")
	require.Contains(t, p, "index.db")
}
