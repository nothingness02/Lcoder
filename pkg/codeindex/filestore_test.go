package codeindex

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snapshot.json")

	s := NewSnapshot()
	s.Files["pkg/a.go"] = FileMeta{ModTime: time.Unix(1, 0), Size: 42}
	s.Nodes = []Node{{ID: "pkg.A", Name: "A", Kind: SymbolKindType}}
	s.Edges = []Edge{{Source: "pkg.A", Target: "pkg.B", Kind: "refers"}}
	s.FailedFiles = []string{"bad.go"}

	require.NoError(t, SaveSnapshot(path, s))
	loaded, err := LoadSnapshot(path)
	require.NoError(t, err)
	require.Equal(t, SnapshotVersion, loaded.Version)
	require.Len(t, loaded.Nodes, 1)
	require.Equal(t, "pkg.A", loaded.Nodes[0].ID)
	require.Len(t, loaded.Edges, 1)
}

func TestLoadMissingSnapshot(t *testing.T) {
	s, err := LoadSnapshot(filepath.Join(t.TempDir(), "missing.json"))
	require.NoError(t, err)
	require.Equal(t, SnapshotVersion, s.Version)
	require.Empty(t, s.Nodes)
}
