package subagent

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
)

func unmarshalEvent[T events.Event](line []byte) (events.Event, error) {
	var e T
	if err := json.Unmarshal(line, &e); err != nil {
		return e, fmt.Errorf("unmarshal %T event: %w", e, err)
	}
	return e, nil
}

// ParseEventLine parses one JSONL event line into a concrete events.Event.
func ParseEventLine(line []byte) (events.Event, error) {
	var disc struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &disc); err != nil {
		return nil, fmt.Errorf("unmarshal event discriminator: %w", err)
	}

	switch events.EventType(disc.Type) {
	case events.AgentStart:
		return unmarshalEvent[events.AgentStartEvent](line)
	case events.AgentEnd:
		return unmarshalEvent[events.AgentEndEvent](line)
	case events.TurnStart:
		return unmarshalEvent[events.TurnStartEvent](line)
	case events.TurnEnd:
		return unmarshalEvent[events.TurnEndEvent](line)
	case events.MessageStart:
		return unmarshalEvent[events.MessageStartEvent](line)
	case events.MessageEnd:
		return unmarshalEvent[events.MessageEndEvent](line)
	case events.MessageUpdate:
		return unmarshalEvent[events.MessageUpdateEvent](line)
	case events.ToolExecutionStart:
		return unmarshalEvent[events.ToolExecutionStartEvent](line)
	case events.ToolExecutionUpdate:
		return unmarshalEvent[events.ToolExecutionUpdateEvent](line)
	case events.ToolExecutionEnd:
		return unmarshalEvent[events.ToolExecutionEndEvent](line)
	case events.Error:
		return unmarshalEvent[events.ErrorEvent](line)
	case events.Audit:
		return unmarshalEvent[events.AuditEvent](line)
	case events.CompactionStarted:
		return unmarshalEvent[events.CompactionStartedEvent](line)
	case events.CompactionCommitted:
		return unmarshalEvent[events.CompactionCommittedEvent](line)
	default:
		return nil, fmt.Errorf("unknown event type: %s", disc.Type)
	}
}

// ExtractFinalAnswer parses JSONL output and returns the last assistant message text.
// If no assistant message is found, it returns an empty string and a nil error.
func ExtractFinalAnswer(output []byte) (string, error) {
	lines := bytes.Split(bytes.TrimSpace(output), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 {
			continue
		}
		ev, err := ParseEventLine(line)
		if err != nil {
			return "", err
		}
		end, ok := ev.(events.AgentEndEvent)
		if !ok {
			continue
		}
		for j := len(end.Messages) - 1; j >= 0; j-- {
			if end.Messages[j].Role == models.RoleAssistant {
				return end.Messages[j].Text(), nil
			}
		}
	}
	return "", nil
}
