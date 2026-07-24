package config

// HookConfig holds declarative hook configuration.
type HookConfig struct {
	Audit              AuditHookConfig              `yaml:"audit"`
	SensitiveFileCheck SensitiveFileCheckHookConfig `yaml:"sensitive_file_check"`
	BashDenylist       BashDenylistHookConfig       `yaml:"bash_denylist"`
}

// AuditHookConfig enables or disables audit logging.
type AuditHookConfig struct {
	Enabled bool `yaml:"enabled"`
}

// SensitiveFileCheckHookConfig blocks access to sensitive paths.
type SensitiveFileCheckHookConfig struct {
	Enabled  bool     `yaml:"enabled"`
	Patterns []string `yaml:"patterns"`
}

// BashDenylistHookConfig blocks dangerous bash substrings.
type BashDenylistHookConfig struct {
	Enabled  bool     `yaml:"enabled"`
	Patterns []string `yaml:"patterns"`
}

// ToolExtensionConfig describes an externally provided tool, either a JSON
// descriptor that creates an HTTPExecutable or a Go plugin (.so) that registers
// tools directly.
type ToolExtensionConfig struct {
	Name          string            `yaml:"name"`
	Type          string            `yaml:"type"`           // "json" or "go-plugin"
	Path          string            `yaml:"path"`           // descriptor file or .so path
	Endpoint      string            `yaml:"endpoint"`       // optional override for JSON endpoint
	Description   string            `yaml:"description"`    // optional override
	Parameters    map[string]any    `yaml:"parameters"`     // optional override
	ExecutionMode string            `yaml:"execution_mode"` // optional override
	Headers       map[string]string `yaml:"headers"`        // optional override
	Config        map[string]any    `yaml:"config"`         // passed to go-plugin New
}

// ExtensionsConfig configures the process-external extension runtime.
type ExtensionsConfig struct {
	Disabled      []string `yaml:"disabled"`
	HookTimeoutMs int      `yaml:"hook_timeout_ms"`
}

// PackageConfig describes an installed package containing modes/skills/tools.
type PackageConfig struct {
	Name   string         `yaml:"name"`
	Source string         `yaml:"source"`
	Path   string         `yaml:"path"`
	Config map[string]any `yaml:"config"`
}
