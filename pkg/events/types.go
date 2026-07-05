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
	ToolExecutionUpdate EventType = "tool_execution_update"
	ToolExecutionEnd    EventType = "tool_execution_end"
	Audit               EventType = "audit"
	Error               EventType = "error"
	CompactionCommitted EventType = "compaction_committed"
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
	EndReasonCompleted  AgentEndReason = "completed"
	EndReasonTerminated AgentEndReason = "terminated"
	EndReasonInterrupted AgentEndReason = "interrupted"
	EndReasonError      AgentEndReason = "error"
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
	Delta   string              `json:"delta"`
	Message models.AgentMessage `json:"message"`
}

// ToolExecutionStartEvent signals that a tool is about to run.
type ToolExecutionStartEvent struct {
	Base
	ToolCallID string         `json:"tool_call_id"`
	ToolName   string         `json:"tool_name"`
	Args       map[string]any `json:"args"`
}

// ToolExecutionUpdateEvent carries a partial tool result.
type ToolExecutionUpdateEvent struct {
	Base
	ToolCallID string `json:"tool_call_id"`
	Partial    string `json:"partial"`
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

// CompactionCommittedEvent signals that the context manager folded older
// messages into a summary and committed the compacted window in place. The
// persistence layer reacts by rewriting the session to the compacted state.
type CompactionCommittedEvent struct{ Base }

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
