// Package extension defines the Lcoder extension interface.
// Extensions can provide additional tools, hooks, and observability exporters.
package extension

import (
	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/observability"
	"github.com/lcoder/lcoder/pkg/tools"
)

// Extension is implemented by community extensions.
type Extension interface {
	// Name returns the extension identifier.
	Name() string
	// RegisterTools registers any custom tools the extension provides.
	RegisterTools(registry *tools.Registry, cwd string) error
	// RegisterHooks returns optional hooks.
	RegisterHooks() (Hooks, error)
	// RegisterExporters returns named exporter factories.
	RegisterExporters() (map[string]observability.ExporterFactory, error)
}

// Hooks groups optional agent hooks provided by an extension.
type Hooks struct {
	BeforeToolCall   agent.BeforeToolCallHook
	AfterToolCall    agent.AfterToolCallHook
	TransformContext agent.TransformContext
	ShouldStop       agent.ShouldStopFunc
}

// Info holds extension metadata from lcoder-extension.yaml.
type Info struct {
	Name         string         `yaml:"name"`
	Version      string         `yaml:"version"`
	Author       string         `yaml:"author"`
	Description  string         `yaml:"description"`
	Entry        string         `yaml:"entry"`
	ConfigSchema map[string]any `yaml:"config_schema"`
}
