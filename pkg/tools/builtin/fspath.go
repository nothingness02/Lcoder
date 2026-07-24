package builtin

import (
	"fmt"
	"path/filepath"
	"strings"
)

// resolveInCwd resolves rawPath against cwd (absolutizing + cleaning) and
// returns the cleaned absolute path.
func resolveInCwd(cwd string, rawPath string) string {
	path := rawPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	return filepath.Clean(path)
}

// walkErrorLog collects traversal/read failures during a directory walk so
// they are surfaced to the model instead of silently swallowed. Only the
// first few examples are kept; the count stays exact.
type walkErrorLog struct {
	count    int
	examples []string
}

func (w *walkErrorLog) record(p string, err error) {
	w.count++
	if len(w.examples) < 3 {
		w.examples = append(w.examples, fmt.Sprintf("%s: %v", p, err))
	}
}

// notice returns the model-facing suffix, or "" when nothing failed.
func (w *walkErrorLog) notice() string {
	if w.count == 0 {
		return ""
	}
	return fmt.Sprintf("\n\n[%d path(s) unreadable: %s]", w.count, strings.Join(w.examples, "; "))
}
