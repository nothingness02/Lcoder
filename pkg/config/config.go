// Package config defines Lcoder configuration types and loading.
package config

import (
	"fmt"
	"os"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	koanf "github.com/knadh/koanf/v2"
	"github.com/lcoder/lcoder/internal/paths"
)

// HTTPToolConfig describes an external HTTP tool.
type HTTPToolConfig struct {
	Name          string            `yaml:"name"`
	Endpoint      string            `yaml:"endpoint"`
	Description   string            `yaml:"description"`
	Parameters    map[string]any    `yaml:"parameters"`
	ExecutionMode string            `yaml:"execution_mode"`
	Headers       map[string]string `yaml:"headers"`
}

// MCPServerConfig describes an MCP server connection.
type MCPServerConfig struct {
	Name      string            `yaml:"name"`
	Transport string            `yaml:"transport"` // "stdio" or "sse"
	Command   []string          `yaml:"command"`
	Env       map[string]string `yaml:"env"`
	URL       string            `yaml:"url"`
	Headers   map[string]string `yaml:"headers"`
	Timeout   int               `yaml:"timeout"` // seconds; 0 -> default
}

// PermissionConfig holds permission rules per tool.
type PermissionConfig struct {
	Rules      map[string]map[string]string `yaml:"rules"`
	UnsafeMode bool                         `yaml:"unsafe_mode"`
}

// TUIConfig holds TUI-specific settings.
type TUIConfig struct {
	Theme string `yaml:"theme"`
}

// ContextConfig controls structured context manager behavior.
type ContextConfig struct {
	MaxTokens        int      `yaml:"max_tokens"`         // hard context budget
	TargetTokens     int      `yaml:"target_tokens"`      // soft target budget
	ReserveOutput    int      `yaml:"reserve_output"`     // output reservation
	MaxOutput        int      `yaml:"max_output"`         // user cap on single-response output tokens (0 = no cap)
	StaticRatio      int      `yaml:"static_ratio"`       // ratio percentage for static/stable blocks
	MinRecent        int      `yaml:"min_recent"`         // minimum recent messages to keep
	KeepRecentTokens int      `yaml:"keep_recent_tokens"` // token budget for the kept tail at proactive pressure (0 = default 20000)
	CompactThreshold float64  `yaml:"compact_threshold"`  // ratio of target at which compaction starts
	CacheHintPolicy  string   `yaml:"cache_hint_policy"`  // "default", "aggressive", "none"
	DeferredTools    bool     `yaml:"deferred_tools"`     // ship only core tools + tool_search
	CoreTools        []string `yaml:"core_tools"`         // tools kept full under deferral
	DropThreshold    float64  `yaml:"drop_threshold"`     // ratio of effective input at which old msgs drop
}

// MemoryProviderConfig describes an external memory provider.
type MemoryProviderConfig struct {
	Name   string             `yaml:"name"`
	Type   string             `yaml:"type"` // "http"
	Config HTTPProviderConfig `yaml:"config"`
}

// HTTPProviderConfig configures a generic HTTP memory provider.
type HTTPProviderConfig struct {
	Endpoint       string            `yaml:"endpoint"`
	APIKey         string            `yaml:"api_key"`
	Headers        map[string]string `yaml:"headers"`
	Timeout        int               `yaml:"timeout"`
	SearchPath     string            `yaml:"search_path"`
	ObservePath    string            `yaml:"observe_path"`
	SessionEndPath string            `yaml:"session_end_path"`
}

// SubagentConfig controls the built-in subagent tool.
type SubagentConfig struct {
	Enabled bool `yaml:"enabled"`
}

// MemoryConfig controls persistent memory behavior.
type MemoryConfig struct {
	Enabled         bool                   `yaml:"enabled"`
	MemoryCharLimit int                    `yaml:"memory_char_limit"`
	UserCharLimit   int                    `yaml:"user_char_limit"`
	DynamicRecall   bool                   `yaml:"dynamic_recall"`
	RecallMaxTokens int                    `yaml:"recall_max_tokens"`
	RecallMinScore  float64                `yaml:"recall_min_score"`
	Providers       []MemoryProviderConfig `yaml:"providers"`
}

// Config is the full Lcoder configuration.
type Config struct {
	Provider       string                  `yaml:"provider"`
	Model          string                  `yaml:"model"`
	Thinking       string                  `yaml:"thinking"`
	ModelsSource   string                  `yaml:"models_source"`
	TUI            TUIConfig               `yaml:"tui"`
	Context        ContextConfig           `yaml:"context"`
	Permissions    PermissionConfig        `yaml:"permissions"`
	HTTPTools      []HTTPToolConfig        `yaml:"http_tools"`
	ToolExtensions []ToolExtensionConfig   `yaml:"tool_extensions"`
	MCPServers     []MCPServerConfig       `yaml:"mcp_servers"`
	Hooks          HookConfig              `yaml:"hooks"`
	Extensions     ExtensionsConfig        `yaml:"extensions"`
	Packages       []PackageConfig         `yaml:"packages"`
	Providers      map[string]ProviderConn `yaml:"providers"`
	Memory         MemoryConfig            `yaml:"memory"`
	CodeIndex      CodeIndexConfig         `yaml:"code_index"`
	Subagent       SubagentConfig          `yaml:"subagent"`
	// Language    string                  `yaml:"language"`
	// Catalog is the shared model metadata loaded from models.yaml (not parsed
	// from the main config file). ModelsConfigPath is its resolved location.
	Catalog          ModelCatalog `yaml:"-"`
	ModelsConfigPath string       `yaml:"-"`
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		Provider: "openai",
		Model:    "gpt-4o-mini",
		TUI:      TUIConfig{Theme: "dark"},
		Context: ContextConfig{
			MaxTokens:        0, // 0 = unset; resolved from catalog/engine at runtime
			TargetTokens:     0, // 0 = unset; derived from MaxTotal when missing
			ReserveOutput:    0, // 0 = unset; falls back to defaultReserveOutput
			MaxOutput:        0, // 0 = unset; no user cap on output tokens
			StaticRatio:      60,
			MinRecent:        10,
			KeepRecentTokens: 20000,
			CompactThreshold: 0.9,
			CacheHintPolicy:  "default",
			DeferredTools:    false,
			CoreTools:        nil,
			DropThreshold:    1.0,
		},
		Memory: MemoryConfig{
			Enabled:         true,
			MemoryCharLimit: 0,
			UserCharLimit:   0,
			DynamicRecall:   true,
			RecallMaxTokens: 1024,
			RecallMinScore:  0.1,
			Providers:       nil,
		},
		CodeIndex: CodeIndexConfig{
			Enabled:    false,
			AutoInject: false,
			Watch:      true,
			MaxResults: 10,
			MaxTokens:  8192,
			Languages:  []string{"go"},
			Exclude:    []string{".git/", ".claude/", ".worktrees/", "reference/", "vendor/", "node_modules/", "*_test.go"},
		},
		Subagent: SubagentConfig{
			Enabled: false,
		},
		Permissions: PermissionConfig{
			Rules: map[string]map[string]string{
				"read":  {"*": "allow"},
				"write": {"*": "allow"},
				"edit":  {"*": "allow"},
				"ls":    {"*": "allow"},
				"grep":  {"*": "allow"},
				"find":  {"*": "allow"},
				"bash": {
					"*": "ask",
					// Read-only / safe commands.
					"ls":             "allow",
					"ls *":           "allow",
					"pwd":            "allow",
					"echo *":         "allow",
					"which *":        "allow",
					"whoami":         "allow",
					"uname *":        "allow",
					"date":           "allow",
					"stat *":         "allow",
					"cat *":          "allow",
					"head *":         "allow",
					"tail *":         "allow",
					"find *":         "allow",
					"grep *":         "allow",
					"git status":     "allow",
					"git status *":   "allow",
					"git log":        "allow",
					"git log *":      "allow",
					"git diff":       "allow",
					"git diff *":     "allow",
					"git show":       "allow",
					"git show *":     "allow",
					"git branch":     "allow",
					"git branch *":   "allow",
					"git remote -v":  "allow",
					"git stash list": "allow",
					"git *":          "allow",
					"go test *":      "allow",
					"go vet *":       "allow",
					"go build *":     "allow",
					"go mod *":       "allow",
					// Dangerous commands.
					"rm -rf /":         "deny",
					"rm -rf /*":        "deny",
					"sudo *":           "deny",
					"su *":             "deny",
					"mkfs.*":           "deny",
					"fdisk *":          "deny",
					"dd":               "deny",
					"dd *":             "deny",
					"chmod -R 777 /":   "deny",
					"chmod -R 777 /*":  "deny",
					"chown -R root /":  "deny",
					"chown -R root /*": "deny",
					"reboot":           "deny",
					"shutdown *":       "deny",
					"halt":             "deny",
					":(){ :|:& };:":    "deny",
				},
			},
		},
	}
}

// CodeIndexConfig configures the repository code-indexing engine.
type CodeIndexConfig struct {
	Enabled    bool     `yaml:"enabled"`
	AutoInject bool     `yaml:"auto_inject"`
	Watch      bool     `yaml:"watch"`
	MaxResults int      `yaml:"max_results"`
	MaxTokens  int      `yaml:"max_tokens"`
	Languages  []string `yaml:"languages"`
	Exclude    []string `yaml:"exclude"`
}

// Budget resolution fallbacks, used only when no explicit user/catalog/discovered
// value is available for the configured model.
const (
	fallbackMaxTokens    = 128000
	defaultReserveOutput = 8192
	defaultTargetRatio   = 0.9
	// fallbackOutputCeiling caps single-response output when the model's real
	// ceiling is unknown from every source. A coding turn must both reason and
	// emit a tool call, so this is generous relative to the 4096 many APIs
	// default to, yet small enough that no gateway rejects it.
	fallbackOutputCeiling = 16384
)

// ResolveContextBudget returns the effective context budget for the configured
// model, plus the source that determined MaxTotal ("user", "catalog",
// "discovered", or "default"). discoveredWindow is the context window looked up
// from the LLM engine/catalog at startup (0 if unknown); discoveredMaxOutput is
// the model's single-response output ceiling discovered the same way (0 if
// unknown). Pass 0 for both for fully offline resolution.
//
// Priority per field:
//
//	MaxTotal:      user context.max_tokens > catalog context_window > discovered window > fallback
//	ReserveOutput: user context.reserve_output > catalog budget.reserve_output > default
//	TargetTotal:   user context.target_tokens > catalog budget.target > MaxTotal * ratio
//	MaxOutput:     min( user context.max_output , [catalog budget.max_output > discovered output > fallback ceiling] )
func (c Config) ResolveContextBudget(discoveredWindow, discoveredMaxOutput int) (TokenBudget, string) {
	cfg := c.Context
	meta, hasMeta := c.Catalog.Lookup(c.Model)

	// MaxTotal.
	maxTotal := cfg.MaxTokens
	source := "user"
	if maxTotal <= 0 && hasMeta && meta.ContextWindow > 0 {
		maxTotal = meta.ContextWindow
		source = "catalog"
	}
	if maxTotal <= 0 && discoveredWindow > 0 {
		maxTotal = discoveredWindow
		source = "discovered"
	}
	if maxTotal <= 0 {
		maxTotal = fallbackMaxTokens
		source = "default"
	}

	// ReserveOutput.
	reserve := cfg.ReserveOutput
	if reserve <= 0 && hasMeta && meta.Budget.ReserveOutput > 0 {
		reserve = meta.Budget.ReserveOutput
	}
	if reserve <= 0 {
		reserve = defaultReserveOutput
	}

	// TargetTotal.
	target := cfg.TargetTokens
	if target <= 0 && hasMeta && meta.Budget.Target > 0 {
		target = meta.Budget.Target
	}
	if target <= 0 || target > maxTotal {
		target = int(float64(maxTotal) * defaultTargetRatio)
	}

	// MaxOutput (the model's single-response output ceiling). Determine the
	// physical ceiling from the most trustworthy source, then clamp by any
	// explicit user cap — never let the user request more than the model emits.
	ceiling := 0
	if hasMeta && meta.Budget.MaxOutput > 0 {
		ceiling = meta.Budget.MaxOutput
	}
	if ceiling <= 0 && discoveredMaxOutput > 0 {
		ceiling = discoveredMaxOutput
	}
	if ceiling <= 0 {
		ceiling = fallbackOutputCeiling
	}
	if cfg.MaxOutput > 0 && cfg.MaxOutput < ceiling {
		ceiling = cfg.MaxOutput
	}

	threshold := cfg.CompactThreshold
	if threshold <= 0 || threshold > 1 {
		threshold = 0.9
	}

	dropThreshold := cfg.DropThreshold
	if dropThreshold <= 0 || dropThreshold > 1 {
		dropThreshold = 1.0
	}

	return TokenBudget{
		MaxTotal:         maxTotal,
		TargetTotal:      target,
		ReserveOutput:    reserve,
		MaxOutput:        ceiling,
		CompactThreshold: threshold,
		DropThreshold:    dropThreshold,
	}, source
}

// TokenBudget is the resolved context window budget.
type TokenBudget struct {
	MaxTotal         int
	TargetTotal      int
	ReserveOutput    int
	MaxOutput        int
	CompactThreshold float64
	DropThreshold    float64
}

// ClampToMaxInput clamps MaxTotal to the model's prompt cap (max_input) and,
// when the clamp pushes MaxTotal below TargetTotal, recomputes TargetTotal
// with the same ratio ResolveContextBudget uses — otherwise compaction would
// never trigger for models whose prompt cap is below the context window.
// source is the ResolveContextBudget source: "user" budgets are explicit and
// never clamped. Returns true when the budget was clamped.
func (b *TokenBudget) ClampToMaxInput(maxInput int, source string) bool {
	if source == "user" || maxInput <= 0 || b.MaxTotal <= maxInput {
		return false
	}
	b.MaxTotal = maxInput
	if b.TargetTotal > b.MaxTotal {
		b.TargetTotal = int(float64(b.MaxTotal) * defaultTargetRatio)
	}
	return true
}

// Load reads configuration from standard locations.
func Load() (Config, error) {
	k := koanf.NewWithConf(koanf.Conf{
		Delim:       ".",
		StrictMerge: false,
	})

	cfg := DefaultConfig()
	_ = k.Load(confmap.Provider(map[string]any{
		"provider":  cfg.Provider,
		"model":     cfg.Model,
		"tui.theme": cfg.TUI.Theme,
		"context": map[string]any{
			"max_tokens":         cfg.Context.MaxTokens,
			"target_tokens":      cfg.Context.TargetTokens,
			"reserve_output":     cfg.Context.ReserveOutput,
			"max_output":         cfg.Context.MaxOutput,
			"static_ratio":       cfg.Context.StaticRatio,
			"min_recent":         cfg.Context.MinRecent,
			"keep_recent_tokens": cfg.Context.KeepRecentTokens,
			"compact_threshold":  cfg.Context.CompactThreshold,
			"cache_hint_policy":  cfg.Context.CacheHintPolicy,
			"deferred_tools":     cfg.Context.DeferredTools,
			"core_tools":         cfg.Context.CoreTools,
			"drop_threshold":     cfg.Context.DropThreshold,
		},
		"memory": map[string]any{
			"enabled":           cfg.Memory.Enabled,
			"memory_char_limit": cfg.Memory.MemoryCharLimit,
			"user_char_limit":   cfg.Memory.UserCharLimit,
			"dynamic_recall":    cfg.Memory.DynamicRecall,
			"recall_max_tokens": cfg.Memory.RecallMaxTokens,
			"recall_min_score":  cfg.Memory.RecallMinScore,
			"providers":         cfg.Memory.Providers,
		},
		"subagent": map[string]any{
			"enabled": cfg.Subagent.Enabled,
		},
	}, "."), nil)

	paths := []string{
		paths.LCoderHome("config.yaml"),
		paths.LCoderHome("config.yml"),
		"lcoder.yaml",
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			if err := k.Load(file.Provider(p), yaml.Parser()); err != nil {
				return cfg, fmt.Errorf("load config %s: %w", p, err)
			}
		}
	}

	_ = k.Load(env.Provider("LCODER_", ".", func(s string) string {
		return s[len("LCODER_"):]
	}), nil)

	if err := k.UnmarshalWithConf("", &cfg, koanf.UnmarshalConf{Tag: "yaml"}); err != nil {
		return cfg, fmt.Errorf("unmarshal config: %w", err)
	}

	// models_source 是嵌套受限 env 映射的平级字段,显式兜底 LCODER_MODELS_SOURCE。
	if v := os.Getenv("LCODER_MODELS_SOURCE"); v != "" {
		cfg.ModelsSource = v
	}

	// Fold TUI-managed credentials (~/.lcoder/credentials.yaml) into providers,
	// without overriding hand-written config.providers fields.
	if credPath := resolveCredentialsPath(); credPath != "" {
		if creds, err := LoadCredentials(credPath); err == nil {
			cfg.Providers = mergeCredentials(cfg.Providers, creds)
		} else {
			fmt.Fprintf(os.Stderr, "warning: 读取 credentials 失败,已忽略: %v\n", err)
		}
	}

	// Expand {env:VAR} references in provider connection settings.
	cfg.Providers = resolveProviders(cfg.Providers)

	// Expand {env:VAR} references in memory provider config.
	cfg.Memory.Providers = resolveMemoryProviders(cfg.Memory.Providers)

	// Fold the shared model catalog (models.yaml) into the config when present,
	// so context budgets and capabilities come from a single source of truth.
	// ResolveContextBudget reads catalog windows directly via Catalog.Lookup.
	if cat, path, ok := LoadModelCatalog(); ok {
		cfg.Catalog = cat
		cfg.ModelsConfigPath = path
	}
	return cfg, nil
}
