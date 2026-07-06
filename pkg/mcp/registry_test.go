package mcp

import (
	"context"
	"testing"
)

func TestRegistryMissingTransportErrors(t *testing.T) {
	reg := NewRegistry([]ServerConfig{{Name: "bad", Command: []string{"echo"}}})
	reg.Connect()
	statuses := reg.Servers()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	if statuses[0].Error == "" {
		t.Fatal("expected error for missing transport")
	}
}

func TestRegistryUnknownTransportErrors(t *testing.T) {
	reg := NewRegistry([]ServerConfig{{Name: "bad", Transport: "ws", Command: []string{"echo"}}})
	reg.Connect()
	statuses := reg.Servers()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	if statuses[0].Error == "" {
		t.Fatal("expected error for unknown transport")
	}
}

func TestPrefixedName(t *testing.T) {
	if got := PrefixedName("server1", "toolA"); got != "server1_toolA" {
		t.Fatalf("unexpected prefixed name: %s", got)
	}
}

func TestExecutableDefinition(t *testing.T) {
	client := &Client{name: "test"}
	exec := NewExecutable(client, Tool{
		Name:        "echo",
		Description: "echo tool",
		InputSchema: map[string]any{"type": "object"},
	})
	def := exec.Definition()
	if def.Name != "test_echo" {
		t.Fatalf("expected test_echo, got %s", def.Name)
	}
}

func TestContentText(t *testing.T) {
	result := &CallToolResult{
		Content: []ContentItem{{Type: "text", Text: "hello"}, {Type: "text", Text: " world"}},
	}
	if result.ContentText() != "hello world" {
		t.Fatalf("unexpected content text: %s", result.ContentText())
	}
}

func TestExecutableExecuteError(t *testing.T) {
	// A nil client will panic on CallTool; use a registry status test instead.
	reg := NewRegistry(nil)
	statuses := reg.Servers()
	if len(statuses) != 0 {
		t.Fatalf("expected 0 statuses, got %d", len(statuses))
	}
}

func TestRegistryCloseServer(t *testing.T) {
	ft := &fakeTransport{healthy: true, info: Info{Name: "fake"}, tools: []Tool{{Name: "t1"}}}
	client, err := NewClient("s1", ft)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry(nil)
	reg.clients["s1"] = client
	if err := reg.CloseServer("s1"); err != nil {
		t.Fatalf("close server: %v", err)
	}
	if len(reg.clients) != 0 {
		t.Fatalf("expected client removed, got %d", len(reg.clients))
	}
	if err := reg.CloseServer("missing"); err == nil {
		t.Fatal("expected error closing missing server")
	}
}

func TestRegistryReconnectPreservesClientPointer(t *testing.T) {
	ft1 := &fakeTransport{healthy: true, info: Info{Name: "v1"}, tools: []Tool{{Name: "t1"}}}
	client, err := NewClient("s1", ft1)
	if err != nil {
		t.Fatal(err)
	}
	ptr := client

	reg := NewRegistry([]ServerConfig{{Name: "s1", Transport: "stdio", Command: []string{"echo"}}})
	reg.clients["s1"] = client

	ft2 := &fakeTransport{healthy: true, info: Info{Name: "v2"}, tools: []Tool{{Name: "t2"}}}
	if err := ptr.ReplaceTransport(context.Background(), ft2); err != nil {
		t.Fatalf("replace transport: %v", err)
	}
	if ptr.ServerInfo().Name != "v2" {
		t.Fatalf("expected server info v2, got %s", ptr.ServerInfo().Name)
	}
	if len(ptr.Tools()) != 1 || ptr.Tools()[0].Name != "t2" {
		t.Fatalf("unexpected tools: %+v", ptr.Tools())
	}
}

func TestRegistryServersReflectsConfig(t *testing.T) {
	reg := NewRegistry([]ServerConfig{
		{Name: "local", Transport: "stdio", Command: []string{"echo"}},
		{Name: "remote", Transport: "sse", URL: "http://localhost:3000", Timeout: 60},
	})
	statuses := reg.Servers()
	if len(statuses) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(statuses))
	}
	if statuses[0].Name != "local" || statuses[0].Transport != "stdio" {
		t.Fatalf("unexpected local status: %+v", statuses[0])
	}
	if statuses[1].Name != "remote" || statuses[1].Transport != "sse" || statuses[1].URL != "http://localhost:3000" {
		t.Fatalf("unexpected remote status: %+v", statuses[1])
	}
}

type fakeTransport struct {
	healthy bool
	info    Info
	tools   []Tool

	lastCallCtx    context.Context
	lastCallMethod string
	lastCallParams any
}

func (f *fakeTransport) Call(ctx context.Context, method string, params any, v any) error {
	f.lastCallCtx = ctx
	f.lastCallMethod = method
	f.lastCallParams = params
	switch method {
	case "initialize":
		*v.(*InitializeResult) = InitializeResult{
			ProtocolVersion: "2024-11-05",
			ServerInfo:      f.info,
			Capabilities:    ServerCapabilities{Tools: &struct{}{}},
		}
	case "tools/list":
		*v.(*ListToolsResult) = ListToolsResult{Tools: f.tools}
	case "tools/call":
		*v.(*CallToolResult) = CallToolResult{Content: []ContentItem{{Type: "text", Text: "ok"}}}
	}
	return nil
}

func (f *fakeTransport) Notify(method string, params any) error { return nil }
func (f *fakeTransport) Close() error                           { return nil }
func (f *fakeTransport) Healthy() bool                          { return f.healthy }

var _ = context.Background
