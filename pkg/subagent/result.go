package subagent

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
)

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
		var e events.AgentStartEvent
		err := json.Unmarshal(line, &e)
		return e, err
	case events.AgentEnd:
		var e events.AgentEndEvent
		err := json.Unmarshal(line, &e)
		return e, err
	case events.TurnStart:
		var e events.TurnStartEvent
		err := json.Unmarshal(line, &e)
		return e, err
	case events.TurnEnd:
		var e events.TurnEndEvent
		err := json.Unmarshal(line, &e)
		return e, err
	case events.MessageStart:
		var e events.MessageStartEvent
		err := json.Unmarshal(line, &e)
		return e, err
	case events.MessageEnd:
		var e events.MessageEndEvent
		err := json.Unmarshal(line, &e)
		return e, err
	case events.MessageUpdate:
		var e events.MessageUpdateEvent
		err := json.Unmarshal(line, &e)
		return e, err
	case events.ToolExecutionStart:
		var e events.ToolExecutionStartEvent
		err := json.Unmarshal(line, &e)
		return e, err
	case events.ToolExecutionUpdate:
		var e events.ToolExecutionUpdateEvent
		err := json.Unmarshal(line, &e)
		return e, err
	case events.ToolExecutionEnd:
		var e events.ToolExecutionEndEvent
		err := json.Unmarshal(line, &e)
		return e, err
	case events.Error:
		var e events.ErrorEvent
		err := json.Unmarshal(line, &e)
		return e, err
	case events.Audit:
		var e events.AuditEvent
		err := json.Unmarshal(line, &e)
		return e, err
	case events.CompactionStarted:
		var e events.CompactionStartedEvent
		err := json.Unmarshal(line, &e)
		return e, err
	case events.CompactionCommitted:
		var e events.CompactionCommittedEvent
		err := json.Unmarshal(line, &e)
		return e, err
	default:
		return nil, fmt.Errorf("unknown event type: %s", disc.Type)
	}
}

// ExtractFinalAnswer parses JSONL output and returns the last assistant message text.
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
