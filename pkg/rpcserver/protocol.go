// Package rpcserver exposes the agentapi.CoreAPI protocol boundary as a
// JSONL RPC transport over stdio, so UIs written in any language can drive
// the agent. It consumes pkg/agentapi and pkg/events — never pkg/tui — plus
// pkg/host for the busy/closed sentinel errors it maps onto stable wire
// texts.
//
// Framing: one JSON object per line, LF-delimited, both directions. Commands
// arrive on stdin; responses, events, and approval requests are written to
// stdout (which nothing else may write while the server runs).
//
// Wire envelopes (snake_case):
//
//	command:  {"id": "cli-1", "type": "prompt", "text": "..."}   (id optional)
//	response: {"type": "response", "id": "cli-1", "ok": true, "data": ...}
//	          {"type": "response", "id": "cli-1", "ok": false, "error": "..."}
//	event:    {"type": "event", "event": {...events.MarshalJSON output...}}
//	approval: {"type": "approval_request", "id": "srv-1", "request": {...}}
//	          answered by {"type": "approval_response", "id": "srv-1",
//	          "result": {"scope": "once"}}
//
// A command without an id is fire-and-forget: it is executed but produces no
// response. Protocol-level errors (malformed JSON, unknown command type)
// always produce an error response so client bugs surface during development.
//
// See docs/rpc-protocol.md for the full command table.
package rpcserver

import (
	"github.com/lcoder/lcoder/pkg/agentapi"
	"github.com/lcoder/lcoder/pkg/models"
)

// maxLineSize bounds a single JSONL record (scanner buffer). Generous enough
// for full transcripts in get_state replies and large tool outputs in events.
const maxLineSize = 16 << 20 // 16 MiB

// commandHead is the first-pass decode of an inbound command line: enough to
// correlate the response and dispatch; the payload is decoded per type.
type commandHead struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// response is the command result envelope.
type response struct {
	Type  string `json:"type"` // always "response"
	ID    string `json:"id,omitempty"`
	OK    bool   `json:"ok"`
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

// eventEnvelope wraps one agent event as emitted by events.MarshalJSON.
type eventEnvelope struct {
	Type  string `json:"type"` // always "event"
	Event any    `json:"event"`
}

// approvalRequest is the reverse-direction envelope asking the client to
// approve a tool call. The id is server-generated ("srv-N"); the client
// answers with an approvalResponseCommand carrying the same id.
type approvalRequest struct {
	Type    string          `json:"type"` // always "approval_request"
	ID      string          `json:"id"`
	Request approvalPayload `json:"request"`
}

// approvalPayload is a snake_case projection of agentapi.ToolCallInfo: the
// fields a remote UI needs to render a permission dialog.
type approvalPayload struct {
	ToolCallID string         `json:"tool_call_id"`
	ToolName   string         `json:"tool_name"`
	Args       map[string]any `json:"args,omitempty"`
	// Command is the bash command line, present only for bash calls.
	Command string `json:"command,omitempty"`
}

// approvalResultPayload is the client's decision on an approval_request.
type approvalResultPayload struct {
	Scope string `json:"scope"` // "deny" | "once" | "session" | "project" | "global"
}

// ---------------------------------------------------------------------------
// Command payloads (decoded from the full command line after the head)
// ---------------------------------------------------------------------------

type textCommand struct {
	Text string `json:"text"`
}

type setModeCommand struct {
	Mode string `json:"mode"`
}

type setModelCommand struct {
	Provider string `json:"provider"`
	// ModelID is keyed "model_id", not "id": the envelope's "id" is reserved
	// for request/response correlation.
	ModelID string `json:"model_id"`
}

type setThinkingCommand struct {
	Value string `json:"value"`
}

type openSessionCommand struct {
	SessionID string `json:"session_id"`
}

type renameSessionCommand struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
}

type truncateAfterCommand struct {
	MessageID string `json:"message_id"`
}

type goalStartCommand struct {
	Objective   string `json:"objective"`
	TurnBudget  int    `json:"turn_budget"`
	TokenBudget int    `json:"token_budget"`
}

type goalPauseCommand struct {
	Reason string `json:"reason"`
}

type restoreCheckpointCommand struct {
	// CheckpointID is keyed "checkpoint_id", not "id" (reserved for
	// request/response correlation, same as set_model's model_id).
	CheckpointID string `json:"checkpoint_id"`
}

type approvalResponseCommand struct {
	ID     string                `json:"id"`
	Result approvalResultPayload `json:"result"`
}

// ---------------------------------------------------------------------------
// get_state snapshot
// ---------------------------------------------------------------------------

// stateSnapshot is the get_state response data: a full bootstrap view a newly
// connected client uses to rebuild its transcript and status chrome. Since v1
// has no event journal/replay, this is the only way to backfill history.
type stateSnapshot struct {
	SessionID string          `json:"session_id"`
	Mode      string          `json:"mode"`
	Thinking  string          `json:"thinking"`
	Model     models.ModelRef `json:"model"`
	// Running reports whether any run is in flight — an ad-hoc prompt/
	// continue run or a goal pursuit (true between pursuit turns too). It
	// comes from the host's Running() when available; it may stay true for a
	// brief moment after agent_end until the run goroutine unwinds.
	Running      bool                  `json:"running"`
	Goal         *agentapi.GoalState   `json:"goal"`
	Tasks        []taskWire            `json:"tasks"`
	ContextStats agentapi.ContextStats `json:"context_stats"`
	// Capabilities lists the model's declared capabilities when known.
	Capabilities []string `json:"capabilities,omitempty"`
	// Messages is the full conversation (agentapi.CoreAPI.AllMessages).
	Messages []models.AgentMessage `json:"messages"`
}

// wireGoal is a nil-safe passthrough: agentapi.GoalState carries its own
// snake_case json tags, so it serializes directly.
func wireGoal(g *agentapi.GoalState) *agentapi.GoalState {
	return g
}

// taskWire is the snake_case projection of task.Task (which has no JSON tags).
type taskWire struct {
	Text   string `json:"text"`
	Status string `json:"status"`
}

// confirmScopeFromWire maps the client's scope string onto ConfirmResult.
// Unknown or empty scopes deny — a malformed answer must never widen a
// permission.
func confirmScopeFromWire(scope string) agentapi.ConfirmResult {
	switch scope {
	case "once":
		return agentapi.ConfirmResult{Allow: true, Scope: agentapi.ScopeOnce}
	case "session":
		return agentapi.ConfirmResult{Allow: true, Scope: agentapi.ScopeSession}
	case "project":
		return agentapi.ConfirmResult{Allow: true, Scope: agentapi.ScopeProject}
	case "global":
		return agentapi.ConfirmResult{Allow: true, Scope: agentapi.ScopeGlobal}
	default: // "deny" and anything unrecognized
		return agentapi.ConfirmResult{Allow: false, Scope: agentapi.ScopeDeny}
	}
}
