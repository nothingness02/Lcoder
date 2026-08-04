package events

// SubagentActivityKind classifies mirrored subagent activity for the TUI.
type SubagentActivityKind string

const (
	SubagentStarted   SubagentActivityKind = "started"
	SubagentText      SubagentActivityKind = "text"       // assistant text delta
	SubagentToolStart SubagentActivityKind = "tool_start" // Text = tool name
	SubagentToolEnd   SubagentActivityKind = "tool_end"   // Text = tool name
	SubagentTurn      SubagentActivityKind = "turn"       // Text = turn number
	SubagentCompleted SubagentActivityKind = "completed"
	SubagentFailed    SubagentActivityKind = "failed" // Text = error
)

// SubagentActivityEvent mirrors one piece of a child agent's activity onto
// the parent's event bus so the TUI can render it nested under the subagent
// tool call (kimi-code's mirrorAgentRun). It is deliberately a simplified,
// flat projection — not the child's raw events — so consumers never see
// interleaved message/tool streams from two agents.
type SubagentActivityEvent struct {
	Base
	AgentID          string               `json:"agent_id"`
	ParentToolCallID string               `json:"parent_tool_call_id"` // the subagent tool call this activity belongs to
	Profile          string               `json:"profile"`
	Kind             SubagentActivityKind `json:"kind"`
	Text             string               `json:"text"` // delta, tool name, turn number, or error text
}

// BackgroundNoticeEvent is emitted when a background subagent finishes, so
// the UI can surface the result immediately instead of waiting for the next
// turn's reminder pull.
type BackgroundNoticeEvent struct {
	Base
	Text string `json:"text"`
}
