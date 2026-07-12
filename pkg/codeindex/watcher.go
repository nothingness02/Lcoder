package codeindex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher watches a project root and incrementally re-indexes the graph when
// source files change. It debounces rapid events so the indexer is not flooded.
type Watcher struct {
	watcher *fsnotify.Watcher
	indexer Indexer
	root    string
	exclude []string
	stop    chan struct{}
	delay   time.Duration
}

// NewWatcher creates a file watcher bound to an indexer.
func NewWatcher(idx Indexer, root string, exclude []string) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &Watcher{
		watcher: fw,
		indexer: idx,
		root:    root,
		exclude: exclude,
		stop:    make(chan struct{}),
		delay:   500 * time.Millisecond,
	}, nil
}

// fileUpdater is implemented by indexers that can re-index a specific set of
// changed paths without walking the whole tree.
type fileUpdater interface {
	UpdateFiles(ctx context.Context, root string, paths []string) error
}

// Start begins watching root. It blocks until Close is called or ctx is cancelled.
func (w *Watcher) Start(ctx context.Context) error {
	if err := w.addTree(w.root); err != nil {
		return err
	}

	var timer *time.Timer
	pending := make(map[string]struct{})
	var mu sync.Mutex

	runUpdate := func() {
		mu.Lock()
		paths := make([]string, 0, len(pending))
		for p := range pending {
			paths = append(paths, p)
		}
		pending = make(map[string]struct{})
		mu.Unlock()
		if len(paths) == 0 {
			return
		}
		if fu, ok := w.indexer.(fileUpdater); ok {
			_ = fu.UpdateFiles(ctx, w.root, paths)
			return
		}
		_ = w.indexer.Update(ctx, w.root)
	}

	trigger := func(rel string) {
		mu.Lock()
		pending[rel] = struct{}{}
		mu.Unlock()
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(w.delay, runUpdate)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.stop:
			return nil
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return nil
			}
			if err != nil {
				return err
			}
		case ev, ok := <-w.watcher.Events:
			if !ok {
				return nil
			}
			rel, _ := filepath.Rel(w.root, ev.Name)
			rel = filepath.ToSlash(rel)
			if w.isExcluded(rel) {
				continue
			}
			if ev.Op&fsnotify.Create == fsnotify.Create {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					_ = w.addTree(ev.Name)
				}
			}
			trigger(rel)
		}
	}
}

// Close stops the watcher.
func (w *Watcher) Close() error {
	close(w.stop)
	return w.watcher.Close()
}

func (w *Watcher) addTree(root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(w.root, path)
		rel = filepath.ToSlash(rel)
		if w.isExcluded(rel) {
			return filepath.SkipDir
		}
		return w.watcher.Add(path)
	})
}

func (w *Watcher) isExcluded(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, p := range w.exclude {
		p = filepath.ToSlash(p)
		if p == "" {
			continue
		}
		if dir, ok := strings.CutSuffix(p, "/"); ok {
			if rel == dir || strings.HasPrefix(rel, dir+"/") {
				return true
			}
			continue
		}
		if matched, _ := filepath.Match(p, rel); matched {
			return true
		}
		if matched, _ := filepath.Match(p, filepath.Base(rel)); matched {
			return true
		}
	}
	return false
}
