package events

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// eventFactories maps every known event type to a constructor for its concrete
// struct. It is the single registry UnmarshalJSON dispatches on; keep it in
// sync with the EventType constants when adding a new event.
var eventFactories = map[EventType]func() Event{
	AgentStart:          func() Event { return &AgentStartEvent{} },
	AgentEnd:            func() Event { return &AgentEndEvent{} },
	TurnStart:           func() Event { return &TurnStartEvent{} },
	TurnEnd:             func() Event { return &TurnEndEvent{} },
	MessageStart:        func() Event { return &MessageStartEvent{} },
	MessageEnd:          func() Event { return &MessageEndEvent{} },
	MessageUpdate:       func() Event { return &MessageUpdateEvent{} },
	ToolExecutionStart:  func() Event { return &ToolExecutionStartEvent{} },
	ToolExecutionEnd:    func() Event { return &ToolExecutionEndEvent{} },
	Audit:               func() Event { return &AuditEvent{} },
	Error:               func() Event { return &ErrorEvent{} },
	CompactionCommitted: func() Event { return &CompactionCommittedEvent{} },
	CompactionStarted:   func() Event { return &CompactionStartedEvent{} },
	SubagentActivity:    func() Event { return &SubagentActivityEvent{} },
	BackgroundNotice:    func() Event { return &BackgroundNoticeEvent{} },
	LLMRetry:            func() Event { return &LLMRetryEvent{} },
	GoalUpdated:         func() Event { return &GoalUpdatedEvent{} },
	TaskListUpdated:     func() Event { return &TaskListUpdatedEvent{} },
}

// UnmarshalJSON decodes a wire-format event (as produced by MarshalJSON) back
// into its concrete Event type, dispatching on the "type" field. The result is
// the VALUE form (e.g. AgentStartEvent, not *AgentStartEvent), matching the
// shape in-process emitters and type switches use — every event's EventType
// is a value receiver, so the value satisfies Event. Unknown or missing types
// return an explicit error.
func UnmarshalJSON(data []byte) (Event, error) {
	var head struct {
		Type EventType `json:"type"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, fmt.Errorf("events: decode event type: %w", err)
	}
	factory, ok := eventFactories[head.Type]
	if !ok {
		return nil, fmt.Errorf("events: unknown event type %q", head.Type)
	}
	ev := factory()
	if err := json.Unmarshal(data, ev); err != nil {
		return nil, fmt.Errorf("events: decode %s event: %w", head.Type, err)
	}
	// Factories allocate pointers for decoding; unwrap to the value form so
	// wire consumers type-switching on values (as in-process subscribers do)
	// match instead of silently missing every event.
	v := reflect.ValueOf(ev)
	if v.Kind() == reflect.Ptr && !v.IsNil() {
		if val, ok := v.Elem().Interface().(Event); ok {
			return val, nil
		}
	}
	return ev, nil
}
