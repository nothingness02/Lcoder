package extension

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"plugin"
	"strings"

	"github.com/lcoder/lcoder/internal/fsutil"
	"github.com/lcoder/lcoder/internal/paths"
)

// PluginLoader loads Go extensions compiled as plugin (.so) files.
// Note: Go plugins are only supported on Linux, macOS, and FreeBSD.
type PluginLoader struct {
	root string
}

// NewPluginLoader creates a plugin loader rooted at the given directory.
func NewPluginLoader(root string) *PluginLoader {
	return &PluginLoader{root: root}
}

// DefaultPluginLoader returns a plugin loader using ~/.lcoder/plugins.
func DefaultPluginLoader() *PluginLoader {
	return NewPluginLoader(paths.LCoderHome("plugins"))
}

// Build compiles a Go extension directory into a plugin .so file.
func (l *PluginLoader) Build(sourceDir, name string) (string, error) {
	if err := fsutil.EnsurePrivateDir(l.root); err != nil {
		return "", err
	}
	out := filepath.Join(l.root, name+".so")
	cmd := exec.Command("go", "build", "-buildmode=plugin", "-o", out, ".")
	cmd.Dir = sourceDir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	if outBytes, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build plugin: %w\n%s", err, string(outBytes))
	}
	return out, nil
}

// Load opens a plugin .so file and calls its New function.
// The plugin must export a symbol named "New" with signature:
//
//	func New(cfg map[string]any) (Extension, error)
func (l *PluginLoader) Load(name string, cfg map[string]any) (Extension, error) {
	path := filepath.Join(l.root, name+".so")
	return l.LoadPath(path, cfg)
}

// LoadPath opens a plugin .so file at an explicit path and calls its New function.
func (l *PluginLoader) LoadPath(path string, cfg map[string]any) (Extension, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("plugin not found: %s", path)
	}
	p, err := plugin.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open plugin: %w", err)
	}
	sym, err := p.Lookup("New")
	if err != nil {
		return nil, fmt.Errorf("plugin missing New symbol: %w", err)
	}
	newFn, ok := sym.(func(map[string]any) (Extension, error))
	if !ok {
		return nil, fmt.Errorf("plugin New has incompatible signature")
	}
	return newFn(cfg)
}

// List returns the names of built plugin files.
func (l *PluginLoader) List() ([]string, error) {
	entries, err := os.ReadDir(l.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".so") {
			names = append(names, strings.TrimSuffix(e.Name(), ".so"))
		}
	}
	return names, nil
}
