package config

import "testing"

// mcp_servers 的 url/headers/env 必须支持 {env:VAR} 插值,与 providers 同一语法。
func TestMCPServersEnvExpansion(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "tvly-test-123")
	t.Setenv("MCP_TOKEN", "tok-abc")

	cfg := Config{
		MCPServers: []MCPServerConfig{
			{
				Name:      "tavily",
				Transport: "stdio",
				Command:   []string{"npx", "-y", "tavily-mcp@latest"},
				Env:       map[string]string{"TAVILY_API_KEY": "{env:TAVILY_API_KEY}"},
			},
			{
				Name:      "remote",
				Transport: "streamable-http",
				URL:       "https://mcp.example.com/mcp/?key={env:MCP_TOKEN}",
				Headers:   map[string]string{"Authorization": "Bearer {env:MCP_TOKEN}"},
			},
		},
	}
	Finalize(&cfg)

	if got := cfg.MCPServers[0].Env["TAVILY_API_KEY"]; got != "tvly-test-123" {
		t.Fatalf("env value not expanded: %q", got)
	}
	if got := cfg.MCPServers[1].URL; got != "https://mcp.example.com/mcp/?key=tok-abc" {
		t.Fatalf("url not expanded: %q", got)
	}
	if got := cfg.MCPServers[1].Headers["Authorization"]; got != "Bearer tok-abc" {
		t.Fatalf("header not expanded: %q", got)
	}
}
