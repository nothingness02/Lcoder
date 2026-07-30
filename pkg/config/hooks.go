package config

// HookConfig holds declarative hook configuration.
// All hooks are shell commands that receive JSON context on stdin:
// exit 0 = allow, exit 2 = block (stderr is the reason).
type HookConfig struct {
	Audit           AuditHookConfig `yaml:"audit"`
	BeforeToolCall  ShellHookConfig `yaml:"before_tool_call"`
	AfterToolResult ShellHookConfig `yaml:"after_tool_result"`
	BeforeCompact   ShellHookConfig `yaml:"before_compact"`
	OnStop          ShellHookConfig `yaml:"on_stop"`
}

// ShellHookConfig runs a shell command as a hook.
// The command receives JSON context on stdin. Exit code semantics:
//
//	0 — allow (stdout logged, stderr discarded)
//	2 — block (stderr becomes reason shown to model)
//	other — allow with warning
//
// Timeout defaults to 30 seconds.
type ShellHookConfig struct {
	Enabled bool   `yaml:"enabled"`
	Command string `yaml:"command"`
	Timeout int    `yaml:"timeout"` // seconds, 0 = default (30)
}

// AuditHookConfig enables or disables audit logging.
type AuditHookConfig struct {
	Enabled bool `yaml:"enabled"`
}

// ToolExtensionConfig describes an externally provided tool: a JSON
// descriptor that creates an HTTPExecutable.
type ToolExtensionConfig struct {
	Name          string            `yaml:"name"`
	Type          string            `yaml:"type"`           // "json"; unknown types are rejected
	Path          string            `yaml:"path"`           // descriptor file path
	Endpoint      string            `yaml:"endpoint"`       // optional override for JSON endpoint
	Description   string            `yaml:"description"`    // optional override
	Parameters    map[string]any    `yaml:"parameters"`     // optional override
	Headers       map[string]string `yaml:"headers"`        // optional override
	Config        map[string]any    `yaml:"config"`         // opaque extension config
}

// ExtensionsConfig configures the process-external extension runtime.
type ExtensionsConfig struct {
	Disabled      []string `yaml:"disabled"`
	HookTimeoutMs int      `yaml:"hook_timeout_ms"`
}
