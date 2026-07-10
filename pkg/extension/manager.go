package extension

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/lcoder/lcoder/internal/fsutil"
	"github.com/lcoder/lcoder/internal/paths"
)

// Manager coordinates installed extensions and packages.
type Manager struct {
	loader *Loader
	root   string
}

// NewManager creates a manager rooted at the given directory.
func NewManager(root string) *Manager {
	return &Manager{
		loader: NewLoader(filepath.Join(root, "extensions")),
		root:   root,
	}
}

// DefaultManager returns a manager using ~/.lcoder.
func DefaultManager() *Manager {
	return NewManager(paths.LCoderHome())
}

// InstallPackage installs a package from a local path or git source.
func (m *Manager) InstallPackage(name, source string) (*Package, error) {
	pkgRoot := filepath.Join(m.root, "packages", name)
	if err := fsutil.EnsurePrivateDir(filepath.Dir(pkgRoot)); err != nil {
		return nil, err
	}
	if _, err := os.Stat(pkgRoot); err == nil {
		return nil, fmt.Errorf("package %q already installed", name)
	}

	// Reuse extension loader's install logic for local/git sources.
	tmpLoader := NewLoader(filepath.Join(m.root, "packages"))
	dir, err := tmpLoader.Install(name, source)
	if err != nil {
		return nil, err
	}

	pkg, err := LoadPackage(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return pkg, nil
}

// UninstallPackage removes an installed package.
func (m *Manager) UninstallPackage(name string) error {
	return os.RemoveAll(filepath.Join(m.root, "packages", name))
}

// ListPackages returns all installed packages.
func (m *Manager) ListPackages() ([]*Package, error) {
	pkgRoot := filepath.Join(m.root, "packages")
	entries, err := os.ReadDir(pkgRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var packages []*Package
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pkg, err := LoadPackage(filepath.Join(pkgRoot, e.Name()))
		if err != nil {
			continue
		}
		packages = append(packages, pkg)
	}
	sort.Slice(packages, func(i, j int) bool {
		return packages[i].Info.Name < packages[j].Info.Name
	})
	return packages, nil
}

// AgentDirs returns all directories containing agent mode definitions.
func (m *Manager) AgentDirs() []string {
	var dirs []string
	if pkgs, err := m.ListPackages(); err == nil {
		for _, p := range pkgs {
			if p.HasAgents() {
				dirs = append(dirs, p.AgentDir())
			}
		}
	}
	return dirs
}

// SkillDirs returns all directories containing skills.
func (m *Manager) SkillDirs() []string {
	var dirs []string
	if pkgs, err := m.ListPackages(); err == nil {
		for _, p := range pkgs {
			if p.HasSkills() {
				dirs = append(dirs, p.SkillDir())
			}
		}
	}
	return dirs
}
