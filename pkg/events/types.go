package events

import (
	"encoding/json"

	"github.com/lcoder/lcoder/pkg/models"
)

// EventType classifies agent lifecycle events.
type EventType string

const (
	AgentStart          EventType = "agent_start"
	AgentEnd            EventType = "agent_end"
	TurnStart           EventType = "turn_start"
	TurnEnd             EventType = "turn_end"
	MessageStart        EventType = "message_start"
	MessageEnd          EventType = "message_end"
	MessageUpdate       EventType = "message_update"
	ToolExecutionStart  EventType = "tool_execution_start"
	ToolExecutionEnd    EventType = "tool_execution_end"
	Audit               EventType = "audit"
	Error               EventType = "error"
	CompactionCommitted EventType = "compaction_committed"
	CompactionStarted   EventType = "compaction_started"
	SubagentActivity    EventType = "subagent_activity"
	SubagentSuspended   EventType = "subagent_suspended"
	BackgroundNotice    EventType = "background_notice"
	LLMRetry            EventType = "llm_retry"
)

// Event is the interface implemented by all agent events.
type Event interface {
	EventType() EventType
}

// Base provides common fields for every event.
type Base struct {
	Type EventType `json:"type"`
	Turn int       `json:"turn"`
}

func (b Base) EventType() EventType { return b.Type }

// AgentStartEvent signals the beginning of an agent run.
type AgentStartEvent struct{ Base }

// AgentEndReason explains why an agent run ended.
type AgentEndReason string

const (
	EndReasonCompleted   AgentEndReason = "completed"
	EndReasonTerminated  AgentEndReason = "terminated"
	EndReasonInterrupted AgentEndReason = "interrupted"
	EndReasonError       AgentEndReason = "error"
	// EndReasonMaxTurns marks a run ended by the MaxTurnsPerRun hard cap.
	// It is a clean boundary, not an error: a GoalDriver may continue the
	// pursuit in a fresh run (kimi-code's isMaxStepsTurnFailure).
	EndReasonMaxTurns AgentEndReason = "max_turns"
)

// AgentEndEvent signals the end of an agent run.
type AgentEndEvent struct {
	Base
	Reason   AgentEndReason        `json:"reason"`
	Messages []models.AgentMessage `json:"messages"`
}

// TurnStartEvent signals the beginning of a provider turn.
type TurnStartEvent struct{ Base }

// TurnEndEvent signals the completion of a provider turn.
type TurnEndEvent struct {
	Base
	Message     models.AgentMessage   `json:"message"`
	ToolResults []models.AgentMessage `json:"tool_results"`
	// Usage is the provider's token accounting for this turn (display and
	// observability only; goal budget accounting happens in the run loop).
	Usage models.LLMUsage `json:"usage"`
}

// MessageStartEvent signals that a message is about to be added.
type MessageStartEvent struct {
	Base
	Message models.AgentMessage `json:"message"`
}

// MessageEndEvent signals that a message has been finalized.
type MessageEndEvent struct {
	Base
	Message models.AgentMessage `json:"message"`
}

// MessageUpdateEvent carries a streaming delta.
type MessageUpdateEvent struct {
	Base
	Delta      string `json:"delta"`
	IsThinking bool   `json:"is_thinking"`
	// IsToolCall marks deltas that carry streamed tool-call argument JSON.
	// They must not be rendered as assistant text (the raw JSON would leak
	// into the visible transcript); UI consumers skip them.
	IsToolCall bool                `json:"is_tool_call"`
	Message    models.AgentMessage `json:"message"`
}

// ToolExecutionStartEvent signals that a tool is about to run.
type ToolExecutionStartEvent struct {
	Base
	ToolCallID string         `json:"tool_call_id"`
	ToolName   string         `json:"tool_name"`
	Args       map[string]any `json:"args"`
}

// ToolExecutionEndEvent signals that a tool has finished.
type ToolExecutionEndEvent struct {
	Base
	ToolCallID string                     `json:"tool_call_id"`
	ToolName   string                     `json:"tool_name"`
	Result     models.ToolExecutionResult `json:"result"`
	IsError    bool                       `json:"is_error"`
}

// ErrorEvent reports a non-fatal runtime error.
type ErrorEvent struct {
	Base
	Message string `json:"message"`
}

// CompactionStartedEvent signals that a blocking compaction is about to run.
// The agent loop emits it right before MaybeCompactLeveled (which runs
// synchronously), so UIs can show a "compacting" indicator for the duration.
type CompactionStartedEvent struct{ Base }

// CompactionCommittedEvent signals that the context manager folded older
// messages into a summary and committed the compacted window in place. The
// persistence layer reacts by appending a CompactionEntry to the session
// (append-only; raw messages are never discarded).
//
// Degraded=true means the circuit breaker was open, so the older span was
// truncated without a real summary and Summary carries an explicit
// summary-unavailable notice instead. Persistence must still record the entry:
// the fold did drop those messages from the live context, and skipping the entry
// would leave the session's compacted view claiming they are still active, so a
// resume would replay them and undo the pressure the fold relieved.
type CompactionCommittedEvent struct {
	Base
	Summary      string `json:"summary,omitempty"`
	FirstKeptID  string `json:"first_kept_entry_id,omitempty"`
	TokensBefore int    `json:"tokens_before,omitempty"`
	// TokensAfter is the context size right after the fold, so the UI can show a
	// before/after comparison. Zero when the emitter can't measure it.
	TokensAfter int  `json:"tokens_after,omitempty"`
	Degraded    bool `json:"degraded,omitempty"`
}

// AuditEvent records a security/permission decision or tool invocation audit.
type AuditEvent struct {
	Base
	ToolCallID  string         `json:"tool_call_id"`
	ToolName    string         `json:"tool_name"`
	Args        map[string]any `json:"args"`
	Decision    string         `json:"decision"`
	Allowed     bool           `json:"allowed"`
	Blocked     bool           `json:"blocked"`
	BlockReason string         `json:"block_reason,omitempty"`
}

// MarshalJSON serializes an event using its concrete fields.
func MarshalJSON(e Event) ([]byte, error) {
	return json.Marshal(e)
}

// LLMRetryEvent signals that an LLM turn is being retried: "establish" for
// transport-level retries (stream establishment) and "turn" for whole-turn
// retries after a pre-content in-stream failure.
type LLMRetryEvent struct {
	Base
	Layer   string `json:"layer"`
	Attempt int    `json:"attempt"`
	WaitMs  int64  `json:"wait_ms"`
	Err     string `json:"err"`
}

// SubagentSuspendedEvent signals that a subagent batch attempt hit a provider
// rate limit and was requeued for a coordinated retry (see subagent/batch).
// The UI can surface it as a pending state on the nested subagent display.
type SubagentSuspendedEvent struct {
	Base
	AgentID string `json:"agent_id"`
	Reason  string `json:"reason"`
}
