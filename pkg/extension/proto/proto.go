// Package proto defines the wire protocol between the Lcoder host and
// process-external extensions: newline-delimited JSON-RPC 2.0 over stdio.
package proto

import "encoding/json"

// ProtocolVersion is the version negotiated in initialize.
const ProtocolVersion = 1

// JSON-RPC 2.0 wire types. A message with Method and no ID is a notification.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return e.Message }

// Host -> extension methods.
const (
	MethodInitialize        = "initialize"
	MethodShutdown          = "shutdown"
	MethodHookToolCall      = "hook/tool_call"
	MethodHookToolResult    = "hook/tool_result"
	MethodHookBeforeCompact = "hook/session_before_compact"
	MethodHookInput         = "hook/input"
	MethodHookStop          = "hook/stop"
	MethodHookSessionStart  = "hook/session_start"
	MethodCommandInvoke     = "command/invoke"
	// EventMethodPrefix prefixes event notifications: "event/<event-type>".
	EventMethodPrefix = "event/"
)

// Extension -> host methods.
const (
	MethodSessionAppendEntry = "session/append_entry"
	MethodSessionGetEntries  = "session/get_entries"
	MethodHostLog            = "host/log"
)

// Hook names as declared in InitializeResult.Hooks.
const (
	HookToolCall      = "tool_call"
	HookToolResult    = "tool_result"
	HookBeforeCompact = "session_before_compact"
	HookInput         = "input"
	HookStop          = "stop"
	HookSessionStart  = "session_start"
)

type InitializeParams struct {
	ProtocolVersion int    `json:"protocol_version"`
	Host            string `json:"host"`
}

type CommandDecl struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage,omitempty"`
}

type InitializeResult struct {
	Name     string        `json:"name"`
	Version  string        `json:"version"`
	Events   []string      `json:"events,omitempty"`
	Hooks    []string      `json:"hooks,omitempty"`
	Commands []CommandDecl `json:"commands,omitempty"`
}

// hook/tool_call: action is "allow" or "block"; Params replaces tool args.
type ToolCallParams struct {
	Tool   string         `json:"tool"`
	Params map[string]any `json:"params"`
}

type ToolCallResult struct {
	Action string         `json:"action"`
	Reason string         `json:"reason,omitempty"`
	Params map[string]any `json:"params,omitempty"`
}

// hook/tool_result: Result replaces the tool result text when non-nil.
type ToolResultParams struct {
	Tool    string         `json:"tool"`
	Params  map[string]any `json:"params"`
	Result  string         `json:"result"`
	IsError bool           `json:"is_error"`
}

type ToolResultResult struct {
	Result *string `json:"result,omitempty"`
}

// hook/session_before_compact.
type BeforeCompactParams struct {
	Conversation string `json:"conversation"`
	TokensBefore int    `json:"tokens_before"`
}

type BeforeCompactResult struct {
	Summary string `json:"summary"`
}

// hook/input: action is "continue", "transform", or "block".
type InputParams struct {
	Text string `json:"text"`
}

type InputResult struct {
	Action string `json:"action"`
	Text   string `json:"text,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// hook/stop: asked when the agent is about to stop (Claude Code Stop hook
// semantics). Continue=true blocks the stop; Reason is fed back to the model.
type StopParams struct {
	Reason string `json:"reason"`
	Turn   int    `json:"turn"`
}

type StopResult struct {
	Continue bool   `json:"continue"`
	Reason   string `json:"reason,omitempty"`
}

// hook/session_start: fires once after the agent and session are ready.
type SessionStartParams struct {
	SessionID string `json:"session_id"`
	Resumed   bool   `json:"resumed"`
}

type SessionStartResult struct {
	Context string `json:"context,omitempty"`
}

// command/invoke.
type CommandInvokeParams struct {
	Name string `json:"name"`
	Args string `json:"args"`
}

type CommandInvokeResult struct {
	Output string `json:"output,omitempty"`
}

// session/append_entry, session/get_entries.
type AppendEntryParams struct {
	CustomType string          `json:"custom_type"`
	Data       json.RawMessage `json:"data"`
}

type Entry struct {
	CustomType string          `json:"custom_type"`
	Data       json.RawMessage `json:"data"`
}

type GetEntriesResult struct {
	Entries []Entry `json:"entries"`
}

// host/log: level is "info", "warn", or "error".
type HostLogParams struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}
