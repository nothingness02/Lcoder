package mcp

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestStreamableHTTPTransportLive exercises a real MCP 2025-03-26 streamable
// HTTP endpoint when MCP_TEST_STREAMABLE_URL is set. It is skipped by default
// so CI does not depend on external services.
func TestStreamableHTTPTransportLive(t *testing.T) {
	url := os.Getenv("MCP_TEST_STREAMABLE_URL")
	if url == "" {
		t.Skip("set MCP_TEST_STREAMABLE_URL to run live streamable HTTP test")
	}

	transport, err := NewStreamableHTTPTransport(url, nil, 30*time.Second)
	if err != nil {
		t.Fatalf("create transport: %v", err)
	}
	defer transport.Close()

	client, err := NewClient("live-fetch", transport)
	if err != nil {
		t.Fatalf("initialize client: %v", err)
	}

	info := client.ServerInfo()
	if info.Name == "" {
		t.Fatal("expected server info name")
	}

	tools := client.Tools()
	if len(tools) == 0 {
		t.Fatal("expected at least one tool")
	}

	// Exercise a tool call if the server exposes fetch.
	ctx := context.Background()
	for _, tool := range tools {
		if tool.Name == "fetch" {
			_, err := client.CallTool(ctx, tool.Name, map[string]any{
				"url": "https://example.com",
			})
			if err != nil {
				t.Fatalf("call fetch: %v", err)
			}
			break
		}
	}
}
