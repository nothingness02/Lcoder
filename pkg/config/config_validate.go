package config

import (
	"fmt"
	"net/url"
	"strings"
)

// Validate checks that the configuration is internally consistent and safe to
// use. It is called immediately after config is loaded, before any subsystems
// are constructed.
func (c Config) Validate() error {
	if c.Provider == "" {
		return fmt.Errorf("provider is required")
	}
	if c.Model == "" {
		return fmt.Errorf("model is required")
	}
	if err := c.Context.Validate(); err != nil {
		return fmt.Errorf("context: %w", err)
	}
	if err := c.Subagent.Validate(); err != nil {
		return fmt.Errorf("subagent: %w", err)
	}
	for i, ht := range c.HTTPTools {
		if err := ht.Validate(); err != nil {
			return fmt.Errorf("http_tools[%d]: %w", i, err)
		}
	}
	for i, m := range c.MCPServers {
		if err := m.Validate(); err != nil {
			return fmt.Errorf("mcp_servers[%d]: %w", i, err)
		}
	}
	if err := c.Permissions.Validate(); err != nil {
		return fmt.Errorf("permissions: %w", err)
	}
	return nil
}

// Validate checks context manager settings.
func (c ContextConfig) Validate() error {
	switch c.CacheHintPolicy {
	case "", "default", "aggressive", "none":
		// ok
	default:
		return fmt.Errorf("cache_hint_policy %q is not valid; must be default, aggressive, or none", c.CacheHintPolicy)
	}
	if c.StaticRatio < 0 || c.StaticRatio > 100 {
		return fmt.Errorf("static_ratio must be between 0 and 100")
	}
	if c.CompactThreshold < 0 || c.CompactThreshold > 1 {
		return fmt.Errorf("compact_threshold must be between 0 and 1")
	}
	if c.DropThreshold < 0 || c.DropThreshold > 1 {
		return fmt.Errorf("drop_threshold must be between 0 and 1")
	}
	if c.MinRecent < 0 {
		return fmt.Errorf("min_recent must be non-negative")
	}
	if c.KeepRecentTokens < 0 {
		return fmt.Errorf("keep_recent_tokens must be non-negative")
	}
	return nil
}

// Validate checks subagent settings.
func (c SubagentConfig) Validate() error {
	return nil
}

// Validate checks an external HTTP tool configuration.
func (c HTTPToolConfig) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(c.Endpoint) == "" {
		return fmt.Errorf("endpoint is required")
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("endpoint %q must be an http or https URL", c.Endpoint)
	}
	switch c.ExecutionMode {
	case "", "parallel", "sequential":
		// ok; empty defaults to parallel at runtime
	default:
		return fmt.Errorf("execution_mode %q is not valid; must be parallel or sequential", c.ExecutionMode)
	}
	return nil
}

// Validate checks an MCP server configuration.
func (c MCPServerConfig) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("name is required")
	}
	switch c.Transport {
	case "", "stdio", "sse", "streamable-http":
		// ok
	default:
		return fmt.Errorf("transport %q is not valid; must be stdio, sse, or streamable-http", c.Transport)
	}
	if c.Transport == "stdio" && len(c.Command) == 0 {
		return fmt.Errorf("command is required for stdio transport")
	}
	if (c.Transport == "sse" || c.Transport == "streamable-http") && strings.TrimSpace(c.URL) == "" {
		return fmt.Errorf("url is required for %s transport", c.Transport)
	}
	if c.Transport != "stdio" && strings.TrimSpace(c.URL) != "" {
		if _, err := url.Parse(c.URL); err != nil {
			return fmt.Errorf("url %q is not valid", c.URL)
		}
	}
	if c.Timeout < 0 {
		return fmt.Errorf("timeout must be non-negative")
	}
	return nil
}

// Validate checks that permission rules only contain known decisions.
func (c PermissionConfig) Validate() error {
	validDecisions := map[string]bool{
		"allow": true,
		"ask":   true,
		"deny":  true,
	}
	for tool, rules := range c.Rules {
		for pattern, decision := range rules {
			if !validDecisions[decision] {
				return fmt.Errorf("rules[%q][%q]: decision %q is not valid; must be allow, ask, or deny", tool, pattern, decision)
			}
		}
	}
	return nil
}
