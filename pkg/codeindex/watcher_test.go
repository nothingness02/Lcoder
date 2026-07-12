package codeindex

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type watcherFakeIndexer struct {
	updated chan string
}

func (f *watcherFakeIndexer) Update(ctx context.Context, root string) error {
	select {
	case f.updated <- root:
	default:
	}
	return nil
}

func (f *watcherFakeIndexer) Search(ctx context.Context, q Query) ([]Result, error) { return nil, nil }
func (f *watcherFakeIndexer) Clear() error                                          { return nil }

func TestWatcherDebouncesEvents(t *testing.T) {
	root := t.TempDir()
	idx := &watcherFakeIndexer{updated: make(chan string, 1)}

	w, err := NewWatcher(idx, root, []string{".git/"})
	require.NoError(t, err)
	defer w.Close()

	go func() {
		_ = w.Start(t.Context())
	}()

	// Allow the watcher to register directories.
	time.Sleep(100 * time.Millisecond)

	path := filepath.Join(root, "demo.go")
	require.NoError(t, os.WriteFile(path, []byte("package demo\n"), 0o644))

	select {
	case r := <-idx.updated:
		require.Equal(t, root, r)
	case <-time.After(3 * time.Second):
		t.Fatal("watcher did not trigger update")
	}
}
