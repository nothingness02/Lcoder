package config

import (
	"strings"
	"testing"
)

func TestValidate_DefaultConfigIsValid(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config should be valid: %v", err)
	}
}

func TestValidate_MissingProvider(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Provider = ""
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "provider is required") {
		t.Fatalf("expected provider error, got %v", err)
	}
}

func TestValidate_MissingModel(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Model = ""
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "model is required") {
		t.Fatalf("expected model error, got %v", err)
	}
}

func TestValidate_InvalidCacheHintPolicy(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Context.CacheHintPolicy = "always"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "cache_hint_policy") {
		t.Fatalf("expected cache hint policy error, got %v", err)
	}
}

func TestValidate_ContextNumericRanges(t *testing.T) {
	cases := []struct {
		name string
		fn   func(*Config)
	}{
		{"static_ratio low", func(c *Config) { c.Context.StaticRatio = -1 }},
		{"static_ratio high", func(c *Config) { c.Context.StaticRatio = 101 }},
		{"compact_threshold low", func(c *Config) { c.Context.CompactThreshold = -0.1 }},
		{"compact_threshold high", func(c *Config) { c.Context.CompactThreshold = 1.1 }},
		{"drop_threshold low", func(c *Config) { c.Context.DropThreshold = -0.1 }},
		{"drop_threshold high", func(c *Config) { c.Context.DropThreshold = 1.1 }},
		{"min_recent negative", func(c *Config) { c.Context.MinRecent = -1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tc.fn(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatalf("expected validation error for %s", tc.name)
			}
		})
	}
}


func TestValidate_InvalidHTTPTool(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HTTPTools = []HTTPToolConfig{{
		Name:     "bad",
		Endpoint: "not-a-url",
	}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "http_tools") {
		t.Fatalf("expected http_tools error, got %v", err)
	}
}

func TestValidate_InvalidHTTPToolExecutionMode(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HTTPTools = []HTTPToolConfig{{
		Name:          "bad",
		Endpoint:      "http://example.com",
		ExecutionMode: "batch",
	}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "execution_mode") {
		t.Fatalf("expected execution_mode error, got %v", err)
	}
}

func TestValidate_InvalidMCPServer(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MCPServers = []MCPServerConfig{{
		Name:      "bad",
		Transport: "stdio",
		Command:   nil,
	}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "mcp_servers") {
		t.Fatalf("expected mcp_servers error, got %v", err)
	}
}

func TestValidate_InvalidPermissionDecision(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Permissions.Rules["write"] = map[string]string{"*": "permit"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "permit") {
		t.Fatalf("expected permissions error, got %v", err)
	}
}



