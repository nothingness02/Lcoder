package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lcoder/lcoder/internal/paths"
	"gopkg.in/yaml.v3"
)

// ModelPricing is per-1,000,000-token pricing in USD.
type ModelPricing struct {
	Prompt     float64 `yaml:"prompt"`
	Completion float64 `yaml:"completion"`
	CacheRead  float64 `yaml:"cache_read"`
	CacheWrite float64 `yaml:"cache_write"`
}

// ModelBudget is the optional context budget for a model.
type ModelBudget struct {
	Target        int `yaml:"target"`
	ReserveOutput int `yaml:"reserve_output"`
	// MaxOutput is the model's official single-response output ceiling (max
	// tokens the API will emit in one turn). 0 = unknown. Overrides any value
	// auto-discovered from models.dev; needed for models the catalog lacks.
	MaxOutput int `yaml:"max_output"`
}

// ModelMeta is one model entry in the shared catalog.
type ModelMeta struct {
	ID            string       `yaml:"id"`
	Provider      string       `yaml:"provider"`
	Aliases       []string     `yaml:"aliases"`
	Capabilities  []string     `yaml:"capabilities"`
	ContextWindow int          `yaml:"context_window"`
	Budget        ModelBudget  `yaml:"budget"`
	Pricing       ModelPricing `yaml:"pricing"`
}

// HasCapability reports whether the model declares the given capability.
func (m ModelMeta) HasCapability(cap string) bool {
	for _, c := range m.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}

// ModelCatalog is the parsed shared model-metadata file.
type ModelCatalog struct {
	Models []ModelMeta `yaml:"models"`
}

// Lookup finds a model by exact id, exact alias, or longest-prefix match
// (so dated variants like claude-sonnet-4-20250514 match "claude-sonnet-4").
func (c ModelCatalog) Lookup(model string) (ModelMeta, bool) {
	for _, m := range c.Models {
		if m.ID == model {
			return m, true
		}
		for _, a := range m.Aliases {
			if a == model {
				return m, true
			}
		}
	}
	for _, m := range c.Models {
		if strings.HasPrefix(m.ID, model) || strings.HasPrefix(model, m.ID) {
			return m, true
		}
	}
	return ModelMeta{}, false
}

// LoadModelCatalogFrom parses a catalog from an explicit path.
func LoadModelCatalogFrom(path string) (ModelCatalog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ModelCatalog{}, err
	}
	var cat ModelCatalog
	if err := yaml.Unmarshal(data, &cat); err != nil {
		return ModelCatalog{}, fmt.Errorf("parse model catalog %s: %w", path, err)
	}
	return cat, nil
}

// resolveModelsConfigPath returns the first existing catalog path among the
// LCODER_MODELS_CONFIG env var, ./configs/models.yaml, and ~/.lcoder/models.yaml.
// The returned path is absolute so it remains valid regardless of the process's
// working directory.
func resolveModelsConfigPath() (string, bool) {
	abs := func(p string) string {
		if a, err := filepath.Abs(p); err == nil {
			return a
		}
		return p
	}
	if p := os.Getenv("LCODER_MODELS_CONFIG"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return abs(p), true
		}
	}
	candidates := []string{filepath.Join("configs", "models.yaml"), paths.LCoderHome("models.yaml")}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return abs(p), true
		}
	}
	return "", false
}

// LoadModelCatalog resolves and loads the shared catalog. The second return is
// the resolved path; the third reports whether a catalog file was found.
func LoadModelCatalog() (ModelCatalog, string, bool) {
	path, ok := resolveModelsConfigPath()
	if !ok {
		return ModelCatalog{}, "", false
	}
	cat, err := LoadModelCatalogFrom(path)
	if err != nil {
		return ModelCatalog{}, path, false
	}
	return cat, path, true
}

// ModelMeta returns the catalog entry for the configured model, if present.
func (c Config) ModelMeta() (ModelMeta, bool) {
	return c.Catalog.Lookup(c.Model)
}

// ModelLacksTools reports whether the configured model is known to the catalog
// but does not declare the "tools" capability. Unknown models return false so we
// never warn about a model we cannot vouch for either way.
func (c Config) ModelLacksTools() bool {
	meta, ok := c.ModelMeta()
	if !ok {
		return false
	}
	return !meta.HasCapability("tools")
}

