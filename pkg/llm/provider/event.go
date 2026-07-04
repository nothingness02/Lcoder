// pkg/llm/provider/event.go
package provider

import (
	"fmt"

	"github.com/lcoder/lcoder/pkg/models"
)

// EventKind enumerates the normalized streaming events every adapter emits,
// independent of the provider's native wire format.
type EventKind int

const (
	KindStart EventKind = iota
	KindTextDelta
	KindThinkingDelta
	KindToolCallDelta
	KindDone
	KindError
)

func (k EventKind) String() string {
	switch k {
	case KindStart:
		return "start"
	case KindTextDelta:
		return "text_delta"
	case KindThinkingDelta:
		return "thinking_delta"
	case KindToolCallDelta:
		return "toolcall_delta"
	case KindDone:
		return "done"
	case KindError:
		return "error"
	default:
		return "unknown"
	}
}

// Event is one normalized streaming event from an adapter.
type Event struct {
	Kind EventKind

	// Delta carries incremental text for KindTextDelta / KindThinkingDelta.
	Delta string

	// ToolCallIndex / ArgumentsJSON describe a streamed tool-call fragment
	// (KindToolCallDelta). Adapters accumulate these and emit the finished
	// tool calls inside Message on KindDone.
	ToolCallIndex int
	ArgumentsJSON string

	// Message is the finalized assistant message on KindDone.
	Message models.AgentMessage

	// Usage is the token/cost usage on KindDone (nil if the provider omitted it).
	Usage *models.LLMUsage

	// Err is set only on KindError.
	Err *EventError
}

// EventError is a classified provider failure carried on KindError.
type EventError struct {
	Code          string         // bad_request | auth | rate_limit | internal
	Message       string         //
	ProviderError map[string]any //
}

// Error implements the error interface so KindError events can be returned directly.
func (e *EventError) Error() string {
	if e == nil {
		return "unknown provider error"
	}
	return fmt.Sprintf("provider error %s: %s", e.Code, e.Message)
}
