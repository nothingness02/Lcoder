package extension

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/lcoder/lcoder/internal/fsutil"
	"github.com/lcoder/lcoder/internal/paths"
)

// Loader installs and loads Lcoder extensions.
type Loader struct {
	root string
}

// NewLoader creates a loader with the given extensions root directory.
func NewLoader(root string) *Loader {
	return &Loader{root: root}
}

// DefaultLoader returns a loader using ~/.lcoder/extensions.
func DefaultLoader() *Loader {
	return NewLoader(paths.LCoderHome("extensions"))
}

// Install installs an extension from a source string.
// Supported sources:
//   - Local path: ./my-ext or /abs/path/to/my-ext
//   - Git repo: github.com/acme/lcoder-ext-tools or https://github.com/acme/lcoder-ext-tools
func (l *Loader) Install(name, source string) (string, error) {
	if err := fsutil.EnsurePrivateDir(l.root); err != nil {
		return "", err
	}
	target := filepath.Join(l.root, name)
	if _, err := os.Stat(target); err == nil {
		return "", fmt.Errorf("extension %q already installed at %s", name, target)
	}

	if isLocal(source) {
		abs, err := filepath.Abs(source)
		if err != nil {
			return "", err
		}
		if err := os.Symlink(abs, target); err != nil {
			// Fallback to copy on Windows if symlink fails.
			if err := l.copyDir(abs, target); err != nil {
				return "", fmt.Errorf("copy extension: %w", err)
			}
		}
		return target, nil
	}

	// Treat as git repository.
	repoURL := source
	if !strings.HasPrefix(repoURL, "http://") && !strings.HasPrefix(repoURL, "https://") && !strings.Contains(repoURL, "://") {
		repoURL = "https://" + repoURL
	}
	cmd := exec.Command("git", "clone", "--depth", "1", repoURL, target)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git clone failed: %w\n%s", err, string(out))
	}
	return target, nil
}

// Uninstall removes an installed extension.
func (l *Loader) Uninstall(name string) error {
	target := filepath.Join(l.root, name)
	return os.RemoveAll(target)
}

// List returns installed extension directories.
func (l *Loader) List() ([]string, error) {
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
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// LoadPath returns the filesystem path for an installed extension.
func (l *Loader) LoadPath(name string) (string, error) {
	target := filepath.Join(l.root, name)
	if _, err := os.Stat(target); err != nil {
		return "", fmt.Errorf("extension %q not found: %w", name, err)
	}
	return target, nil
}

func isLocal(source string) bool {
	if strings.HasPrefix(source, "/") || strings.HasPrefix(source, ".") || strings.HasPrefix(source, "~") {
		return true
	}
	if _, err := os.Stat(source); err == nil {
		return true
	}
	return false
}

func (l *Loader) copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}
