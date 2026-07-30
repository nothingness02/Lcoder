package tools

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/lcoder/lcoder/pkg/config"
)

// LoadExtensions registers tools from JSON descriptors (HTTPExecutable tools).
func (r *Registry) LoadExtensions(cfgs []config.ToolExtensionConfig) error {
	for _, cfg := range cfgs {
		if cfg.Type == "" {
			cfg.Type = "json"
		}
		switch cfg.Type {
		case "json":
			if err := r.loadJSONExtension(cfg); err != nil {
				return err
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
