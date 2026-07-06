package mcp

import (
	"context"
	"fmt"
	"sync"
)

// Client is a transport-agnostic MCP client. It performs protocol
// initialization, caches the server's tool list, and dispatches tool calls.
type Client struct {
	name      string
	transport Transport

	mu         sync.RWMutex
	serverInfo Info
	serverCaps ServerCapabilities
	tools      []Tool
}

// NewClient initializes a client over the provided transport.
func NewClient(name string, transport Transport) (*Client, error) {
	c := &Client{
		name:      name,
		transport: transport,
	}
	if err := c.initialize(context.Background()); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("mcp initialize: %w", err)
	}
	return c, nil
}

// Name returns the server display name.
func (c *Client) Name() string { return c.name }

// ServerInfo returns the server info from initialization.
func (c *Client) ServerInfo() Info {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.serverInfo
}

// Tools returns the cached list of tools from the MCP server.
func (c *Client) Tools() []Tool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tools
}

func (c *Client) initialize(ctx context.Context) error {
	params := InitializeParams{
		ProtocolVersion: "2024-11-05",
		ClientInfo: Info{
			Name:    "lcoder",
			Version: "0.1.0",
		},
	}
	var result InitializeResult
	if err := c.transport.Call(ctx, "initialize", params, &result); err != nil {
		return err
	}
	c.mu.Lock()
	c.serverInfo = result.ServerInfo
	c.serverCaps = result.Capabilities
	c.mu.Unlock()

	// Send initialized notification.
	_ = c.transport.Notify("notifications/initialized", struct{}{})

	// List tools if supported.
	if result.Capabilities.Tools != nil {
		var toolsResult ListToolsResult
		if err := c.transport.Call(ctx, "tools/list", struct{}{}, &toolsResult); err != nil {
			return err
		}
		c.mu.Lock()
		c.tools = toolsResult.Tools
		c.mu.Unlock()
	}

	return nil
}

// CallTool invokes an MCP tool by name.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (*CallToolResult, error) {
	params := CallToolParams{Name: name, Arguments: args}
	var result CallToolResult
	if err := c.transport.Call(ctx, "tools/call", params, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Close shuts down the MCP transport.
func (c *Client) Close() error {
	if c.transport == nil {
		return nil
	}
	return c.transport.Close()
}

// Healthy reports whether the underlying transport is still usable.
func (c *Client) Healthy() bool {
	if c.transport == nil {
		return false
	}
	return c.transport.Healthy()
}

// ReplaceTransport closes the current transport, initializes a new one, and
// refreshes cached server info/tools. The Client pointer stays stable so
// existing tool executables remain valid.
func (c *Client) ReplaceTransport(ctx context.Context, transport Transport) error {
	info, caps, tools, err := c.initTransport(ctx, transport)
	if err != nil {
		_ = transport.Close()
		return err
	}
	c.mu.Lock()
	old := c.transport
	c.transport = transport
	c.serverInfo = info
	c.serverCaps = caps
	c.tools = tools
	c.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return nil
}

func (c *Client) initTransport(ctx context.Context, transport Transport) (Info, ServerCapabilities, []Tool, error) {
	params := InitializeParams{
		ProtocolVersion: "2024-11-05",
		ClientInfo: Info{
			Name:    "lcoder",
			Version: "0.1.0",
		},
	}
	var result InitializeResult
	if err := transport.Call(ctx, "initialize", params, &result); err != nil {
		return Info{}, ServerCapabilities{}, nil, err
	}
	_ = transport.Notify("notifications/initialized", struct{}{})

	var tools []Tool
	if result.Capabilities.Tools != nil {
		var toolsResult ListToolsResult
		if err := transport.Call(ctx, "tools/list", struct{}{}, &toolsResult); err != nil {
			return Info{}, ServerCapabilities{}, nil, err
		}
		tools = toolsResult.Tools
	}
	return result.ServerInfo, result.Capabilities, tools, nil
}
