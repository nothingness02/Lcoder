package tui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sahilm/fuzzy"
)

const (
	// fileIndexMaxScan caps how many entries one scan collects; beyond this a
	// huge repo gets a truncated index rather than a stalled TUI.
	fileIndexMaxScan = 20000
	// fileIndexTTL is how long a completed scan stays fresh. A stale index is
	// rescanned in the background on the next mention while the old list keeps
	// serving, so completion never goes through an empty window.
	fileIndexTTL = 30 * time.Second
)

// fileSuggester backs @-file completion. The FileIndex implementation caches a
// background walk; an fd-backed implementation (fd.go) answers per query.
type fileSuggester interface {
	// EnsureStarted kicks off (or refreshes) the backend. Cheap no-op when the
	// cached data is fresh or a scan is already running.
	EnsureStarted()
	// Stop releases background resources (cancels in-flight scans).
	Stop()
	// Ready reports whether Matches returns real results; when false the menu
	// shows an "indexing…" placeholder instead of items.
	Ready() bool
	// Matches returns up to limit cached paths fuzzy-matching partial.
	Matches(partial string, limit int) []string
}

// newFileSuggester picks the @-completion backend for cwd: an fd subprocess
// when the binary is available (fd.go), else the cached FileIndex.
func newFileSuggester(cwd string) fileSuggester {
	if bin := detectFd(exec.LookPath, probeFdVersion); bin != "" {
		return newFdSuggester(cwd, bin)
	}
	return NewFileIndex(cwd)
}

// FileIndex caches the cwd-relative file and directory list, walking once in
// the background so the per-keystroke path does zero filesystem IO.
type FileIndex struct {
	cwd string
	now func() time.Time // injectable clock for TTL tests

	mu        sync.RWMutex
	files     []string
	ready     bool
	scannedAt time.Time
	running   bool
	gen       int // generation counter; results from superseded scans are dropped

	ctx    context.Context
	cancel context.CancelFunc
}

// NewFileIndex creates an index for cwd. Call EnsureStarted to trigger the
// first scan and Stop to release it.
func NewFileIndex(cwd string) *FileIndex {
	ctx, cancel := context.WithCancel(context.Background())
	return &FileIndex{cwd: cwd, now: time.Now, ctx: ctx, cancel: cancel}
}

// EnsureStarted starts a background scan unless one is running or the cached
// list is still fresh (younger than fileIndexTTL).
func (ix *FileIndex) EnsureStarted() {
	ix.mu.Lock()
	if ix.running || (ix.ready && ix.now().Sub(ix.scannedAt) < fileIndexTTL) {
		ix.mu.Unlock()
		return
	}
	ix.running = true
	ix.gen++
	gen := ix.gen
	ix.mu.Unlock()

	go func() {
		files, err := scanFiles(ix.ctx, ix.cwd, fileIndexMaxScan)
		ix.mu.Lock()
		defer ix.mu.Unlock()
		ix.running = false
		if err != nil || gen != ix.gen {
			// Cancelled or superseded by a newer scan: keep serving the old list.
			return
		}
		ix.files = files
		ix.ready = true
		ix.scannedAt = ix.now()
	}()
}

// Stop cancels any in-flight scan. A stopped index never becomes ready.
func (ix *FileIndex) Stop() { ix.cancel() }

// Ready reports whether the index has a completed scan to serve.
func (ix *FileIndex) Ready() bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.ready
}

// Matches fuzzy-matches partial against the cached list. It returns nil while
// the index is not ready.
func (ix *FileIndex) Matches(partial string, limit int) []string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	if !ix.ready {
		return nil
	}
	return fuzzyMatchPaths(ix.files, partial, limit)
}

// fuzzyMatchPaths ranks files against partial, capping the result at limit. An
// empty partial returns the leading entries uncapped by score.
func fuzzyMatchPaths(files []string, partial string, limit int) []string {
	if partial == "" {
		out := files
		if len(out) > limit {
			out = out[:limit]
		}
		return out
	}
	var out []string
	for _, m := range fuzzy.Find(partial, files) {
		out = append(out, files[m.Index])
		if len(out) >= limit {
			break
		}
	}
	return out
}

// errScanCapped stops the walk early once maxScan entries are collected.
var errScanCapped = errors.New("fileindex: scan cap reached")

// scanFiles walks cwd collecting slash-separated relative paths of files and
// directories (mentions can target either), skipping .git, node_modules, and
// hidden directories. It honours ctx cancellation and stops early after
// maxScan entries.
func scanFiles(ctx context.Context, cwd string, maxScan int) ([]string, error) {
	files := make([]string, 0, 1024)
	err := filepath.WalkDir(cwd, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			if path == cwd {
				return nil
			}
			name := d.Name()
			if name == ".git" || name == "node_modules" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
		}
		rel, err := filepath.Rel(cwd, path)
		if err != nil {
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		if len(files) >= maxScan {
			return errScanCapped
		}
		return nil
	})
	if err == errScanCapped {
		return files, nil
	}
	return files, err
}
