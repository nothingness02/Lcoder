package agent

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lcoder/lcoder"
	"gopkg.in/yaml.v3"
)

// ModeConfig describes an agent mode.
type ModeConfig struct {
	Name          string   `yaml:"name"`
	Description   string   `yaml:"description"`
	SystemPrompt  string   `yaml:"system_prompt"`
	AllowedTools  []string `yaml:"allowed_tools,omitempty"`
	DeniedTools   []string `yaml:"denied_tools,omitempty"`
	Model         string   `yaml:"model,omitempty"`
	Provider      string   `yaml:"provider,omitempty"`
	ExecutionMode string   `yaml:"execution_mode,omitempty"`
}

// ModeManager loads and selects agent modes.
type ModeManager struct {
	modes map[string]ModeConfig
}

// NewModeManager creates a mode manager preloaded with the embedded default
// modes. Filesystem modes loaded later via LoadModes override defaults by name.
func NewModeManager() *ModeManager {
	mm := &ModeManager{modes: make(map[string]ModeConfig)}
	_ = loadEmbeddedModes(mm)
	return mm
}

func loadEmbeddedModes(mm *ModeManager) error {
	entries, err := lcoder.AgentModes.ReadDir("configs/agents")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !(strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")) {
			continue
		}
		data, err := lcoder.AgentModes.ReadFile(path.Join("configs/agents", name))
		if err != nil {
			return err
		}
		if err := mm.loadModeData(data, name); err != nil {
			return err
		}
	}
	return nil
}

// LoadModes loads mode configs from the provided directories.
// Later directories override earlier ones for the same mode name.
func (mm *ModeManager) LoadModes(dirs []string) error {
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !(strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")) {
				continue
			}
			path := filepath.Join(dir, name)
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if err := mm.loadModeData(data, name); err != nil {
				return fmt.Errorf("invalid mode config %s: %w", path, err)
			}
		}
	}
	return nil
}

func (mm *ModeManager) loadModeData(data []byte, filename string) error {
	var mode ModeConfig
	if err := yaml.Unmarshal(data, &mode); err != nil {
		return err
	}
	if mode.Name == "" {
		mode.Name = strings.TrimSuffix(filename, filepath.Ext(filename))
	}
	mm.modes[mode.Name] = mode
	return nil
}

// Get returns a mode by name. If not found, it returns the default code mode.
func (mm *ModeManager) Get(name string) ModeConfig {
	if mode, ok := mm.modes[name]; ok {
		return mode
	}
	if mode, ok := mm.modes["code"]; ok {
		return mode
	}
	return ModeConfig{Name: "code", Description: "Default coding mode", ExecutionMode: "parallel"}
}

// List returns all loaded modes sorted by name.
func (mm *ModeManager) List() []ModeConfig {
	var names []string
	for name := range mm.modes {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []ModeConfig
	for _, name := range names {
		out = append(out, mm.modes[name])
	}
	return out
}

// Detect selects a mode based on the user prompt.
func (mm *ModeManager) Detect(prompt string) string {
	lower := strings.ToLower(prompt)
	switch {
	case containsAny(lower, []string{"plan", "design", "architecture", "roadmap", "strategy"}):
		return mm.fallback("plan")
	case containsAny(lower, []string{"test", "testing", "spec", "unit test", "assert"}):
		return mm.fallback("test")
	case containsAny(lower, []string{"review", "audit", "check", "inspect", "critique"}):
		return mm.fallback("review")
	case containsAny(lower, []string{"explore", "find", "search", "discover", "lookup"}):
		return mm.fallback("explore")
	default:
		return mm.fallback("code")
	}
}

func (mm *ModeManager) fallback(name string) string {
	if _, ok := mm.modes[name]; ok {
		return name
	}
	if _, ok := mm.modes["code"]; ok {
		return "code"
	}
	return name
}

func containsAny(s string, keywords []string) bool {
	for _, k := range keywords {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// DefaultModeDirs returns the default directories to search for mode configs.
func DefaultModeDirs(cwd string) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return []string{
		filepath.Join(cwd, "configs", "agents"),
		filepath.Join(home, ".lcoder", "agents"),
	}
}
