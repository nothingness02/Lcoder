package mcp

import "context"

// Transport abstracts the JSON-RPC wire layer for an MCP server.
// Both local stdio subprocesses and remote HTTP+SSE servers implement this
// interface so that Client can remain transport-agnostic.
type Transport interface {
	// Call sends a JSON-RPC request and unmarshals the result into v.
	Call(ctx context.Context, method string, params any, v any) error
	// Notify sends a JSON-RPC notification (no response expected).
	Notify(method string, params any) error
	// Close shuts down the transport.
	Close() error
	// Healthy reports whether the transport is still usable.
	Healthy() bool
}
