package config

import (
	"io"
	"os"
	"strings"
	"testing"
)

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

// 未设置的变量必须产生 warning——检测要先于展开,否则 {env:...} 模式被
// 展开抹掉后 warning 永远不会触发(顺序写反过的回归)。
func TestFinalizeWarnsOnUnsetMCPEnvRef(t *testing.T) {
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = old })

	cfg := Config{MCPServers: []MCPServerConfig{{
		Name: "remote", URL: "https://h/{env:DEFINITELY_UNSET_MCP_VAR}",
	}}}
	Finalize(&cfg)

	_ = w.Close()
	out, _ := io.ReadAll(r)
	if !strings.Contains(string(out), "DEFINITELY_UNSET_MCP_VAR") {
		t.Fatalf("missing-var warning not emitted: %q", out)
	}
}
