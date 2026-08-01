// Package mcp implements a Model Context Protocol (MCP) client adapter.
//
// MCP is a JSON-RPC 2.0 based protocol. This package supports the stdio
// transport and exposes MCP tools as Lcoder tools via the tools.Registry.
package mcp

import "encoding/json"

// Request is a JSON-RPC request.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *ErrorObject    `json:"error,omitempty"`
}

// ErrorObject is a JSON-RPC error.
type ErrorObject struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Error implements the error interface.
func (e *ErrorObject) Error() string {
	return e.Message
}

// InitializeParams is sent on initialize.
type InitializeParams struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ClientCapabilities `json:"capabilities"`
	ClientInfo      Info               `json:"clientInfo"`
}

// ClientCapabilities describes client capabilities.
type ClientCapabilities struct {
	Roots    *struct{} `json:"roots,omitempty"`
	Sampling *struct{} `json:"sampling,omitempty"`
}

// Info identifies a client or server.
type Info struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeResult is returned by initialize.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      Info               `json:"serverInfo"`
}

// ServerCapabilities describes server capabilities.
type ServerCapabilities struct {
	Tools *struct{} `json:"tools,omitempty"`
}

// ListToolsResult is returned by tools/list.
type ListToolsResult struct {
	Tools []Tool `json:"tools"`
}

// Tool describes an MCP tool.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
}

// CallToolParams is sent by tools/call.
type CallToolParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// CallToolResult is returned by tools/call.
type CallToolResult struct {
	Content []ContentItem `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// ContentItem is a content part in a tool result.
type ContentItem struct {
	Type        string            `json:"type"`
	Text        string            `json:"text,omitempty"`
	Data        string            `json:"data,omitempty"`
	MimeType    string            `json:"mimeType,omitempty"`
	URI         string            `json:"uri,omitempty"`
	Name        string            `json:"name,omitempty"`
	Description string            `json:"description,omitempty"`
	Resource    *EmbeddedResource `json:"resource,omitempty"`
}

// EmbeddedResource is the payload of a "resource" content item.
type EmbeddedResource struct {
	URI      string `json:"uri,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}
