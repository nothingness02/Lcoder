package tools

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/models"
)

// ToolExtension is the subset of an extension required to register its tools.
// It is implemented by loaded go-plugin extensions.
type ToolExtension interface {
	RegisterTools(registry *Registry, cwd string) error
}

// ToolExtensionPluginLoader loads go-plugin tool extensions. It is supplied by
// the caller (cmd/lcoder/main.go) to avoid a package import cycle with
// pkg/extension.
type ToolExtensionPluginLoader interface {
	LoadPlugin(path string, cfg map[string]any) (ToolExtension, error)
}

// LoadExtensions registers tools from the provided extension configs. JSON
// descriptors become HTTPExecutable tools; go-plugin extensions are loaded via
// pluginLoader and asked to register their own tools.
func (r *Registry) LoadExtensions(cfgs []config.ToolExtensionConfig, pluginLoader ToolExtensionPluginLoader) error {
	for _, cfg := range cfgs {
		if cfg.Type == "" {
			cfg.Type = "json"
		}
		switch cfg.Type {
		case "json":
			if err := r.loadJSONExtension(cfg); err != nil {
				return err
			}
		case "go-plugin":
			if pluginLoader == nil {
				return fmt.Errorf("tool extension %q requires a plugin loader", cfg.Name)
			}
			ext, err := pluginLoader.LoadPlugin(cfg.Path, cfg.Config)
			if err != nil {
				return fmt.Errorf("load tool extension %q: %w", cfg.Name, err)
			}
			if err := ext.RegisterTools(r, r.cwd); err != nil {
				return fmt.Errorf("register tools from extension %q: %w", cfg.Name, err)
			}
		default:
			return fmt.Errorf("unknown tool extension type %q for %q", cfg.Type, cfg.Name)
		}
	}
	return nil
}

func (r *Registry) loadJSONExtension(cfg config.ToolExtensionConfig) error {
	if cfg.Path == "" {
		return fmt.Errorf("tool extension %q: path is required for json type", cfg.Name)
	}
	data, err := os.ReadFile(cfg.Path)
	if err != nil {
		return fmt.Errorf("tool extension %q: read descriptor: %w", cfg.Name, err)
	}
	var httpCfg HTTPConfig
	if err := json.Unmarshal(data, &httpCfg); err != nil {
		return fmt.Errorf("tool extension %q: parse descriptor: %w", cfg.Name, err)
	}
	if cfg.Name != "" {
		httpCfg.Name = cfg.Name
	}
	if cfg.Endpoint != "" {
		httpCfg.Endpoint = ExpandEndpointEnv(cfg.Endpoint)
	}
	if cfg.Description != "" {
		httpCfg.Description = cfg.Description
	}
	if cfg.Parameters != nil {
		httpCfg.Parameters = cfg.Parameters
	}
	if cfg.ExecutionMode != "" {
		httpCfg.ExecutionMode = models.ExecutionMode(cfg.ExecutionMode)
	}
	if cfg.Headers != nil {
		httpCfg.Headers = cfg.Headers
	}
	if httpCfg.Endpoint != "" {
		httpCfg.Endpoint = ExpandEndpointEnv(httpCfg.Endpoint)
	}
	if httpCfg.Name == "" {
		return fmt.Errorf("tool extension %q: name is required", cfg.Name)
	}
	r.Register(httpCfg.Name, NewHTTPExecutable(httpCfg))
	return nil
}
