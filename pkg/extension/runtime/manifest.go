package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// Manifest describes how to start one extension process (extension.yaml).
type Manifest struct {
	Name    string            `yaml:"name"`
	Version string            `yaml:"version"`
	Command []string          `yaml:"command"`
	Env     map[string]string `yaml:"env"`
	// Dir is the directory containing extension.yaml; used as the process cwd.
	Dir string `yaml:"-"`
}

// Discover returns the manifests of all extensions under root
// (root/<name>/extension.yaml), sorted by name. A missing root is not an
// error; a malformed manifest is.
func Discover(root string) ([]Manifest, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Manifest
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(root, e.Name(), "extension.yaml")
		data, err := os.ReadFile(path)
		if err != nil {
			continue // not an extension directory
		}
		var m Manifest
		if err := yaml.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("extension manifest %s: %w", path, err)
		}
		if m.Name == "" || len(m.Command) == 0 {
			return nil, fmt.Errorf("extension manifest %s: name and command are required", path)
		}
		m.Dir = filepath.Join(root, e.Name())
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
