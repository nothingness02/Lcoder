package events

import (
	"reflect"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/task"
)

// roundTripSamples holds one non-zero-value instance of every known event
// type. Keep it in sync with eventFactories in json.go.
func roundTripSamples() []Event {
	msg := models.AgentMessage{
		ID:        "m1",
		Role:      models.RoleAssistant,
		Content:   []models.ContentPart{models.TextContent{Text: "hello"}},
		Timestamp: 1700000000000,
	}
	toolMsg := models.AgentMessage{
		ID:   "m2",
		Role: models.RoleToolResult,
		Content: []models.ContentPart{models.ToolResultContent{
			ToolCallID: "c1",
			Name:       "bash",
			Content:    []models.ContentPart{models.TextContent{Text: "ok"}},
		}},
		Timestamp: 1700000000001,
	}
	return []Event{
		AgentStartEvent{Base: Base{Type: AgentStart, Turn: 1}},
		AgentEndEvent{
			Base:     Base{Type: AgentEnd, Turn: 2},
			Reason:   EndReasonMaxTurns,
			Messages: []models.AgentMessage{msg},
		},
		TurnStartEvent{Base: Base{Type: TurnStart, Turn: 3}},
		TurnEndEvent{
			Base:        Base{Type: TurnEnd, Turn: 4},
			Message:     msg,
			ToolResults: []models.AgentMessage{toolMsg},
			Usage: models.LLMUsage{
				Provider:         "p",
				Model:            "m",
				PromptTokens:     10,
				CompletionTokens: 20,
				TotalTokens:      30,
				CacheReadTokens:  5,
				CacheWriteTokens: 6,
				PromptCost:       0.5,
				CompletionCost:   1.5,
				CacheReadCost:    0.25,
				CacheWriteCost:   0.75,
				TotalCost:        3.0,
			},
		},
		MessageStartEvent{Base: Base{Type: MessageStart, Turn: 5}, Message: msg},
		MessageEndEvent{Base: Base{Type: MessageEnd, Turn: 6}, Message: msg},
		MessageUpdateEvent{
			Base:       Base{Type: MessageUpdate, Turn: 7},
			Delta:      "chunk",
			IsThinking: true,
			IsToolCall: true,
			Message:    msg,
		},
		ToolExecutionStartEvent{
			Base:       Base{Type: ToolExecutionStart, Turn: 8},
			ToolCallID: "c1",
			ToolName:   "bash",
			Args:       map[string]any{"command": "ls", "force": true},
		},
		ToolExecutionEndEvent{
			Base:       Base{Type: ToolExecutionEnd, Turn: 9},
			ToolCallID: "c1",
			ToolName:   "bash",
			Result: models.ToolExecutionResult{
				Content:   []models.ContentPart{models.TextContent{Text: "done"}},
				Details:   map[string]any{"exit": "0"},
				Terminate: true,
				IsError:   true,
			},
			IsError: true,
		},
		AuditEvent{
			Base:        Base{Type: Audit, Turn: 10},
			ToolCallID:  "c2",
			ToolName:    "edit",
			Args:        map[string]any{"path": "x.go"},
			Decision:    "allow",
			Allowed:     true,
			Blocked:     true,
			BlockReason: "policy",
		},
		ErrorEvent{Base: Base{Type: Error, Turn: 11}, Message: "boom"},
		CompactionStartedEvent{Base: Base{Type: CompactionStarted, Turn: 12}},
		CompactionCommittedEvent{
			Base:         Base{Type: CompactionCommitted, Turn: 13},
			Summary:      "summary",
			FirstKeptID:  "e9",
			TokensBefore: 1000,
			TokensAfter:  400,
			Degraded:     true,
		},
		SubagentActivityEvent{
			Base:             Base{Type: SubagentActivity, Turn: 14},
			AgentID:          "sub-1",
			ParentToolCallID: "c3",
			Profile:          "explore",
			Kind:             SubagentToolStart,
			Text:             "grep",
		},
		BackgroundNoticeEvent{Base: Base{Type: BackgroundNotice, Turn: 15}, Text: "finished"},
		LLMRetryEvent{
			Base:    Base{Type: LLMRetry, Turn: 16},
			Layer:   "turn",
			Attempt: 2,
			WaitMs:  1500,
			Err:     "connection reset",
		},
		GoalUpdatedEvent{
			Base:        Base{Type: GoalUpdated, Turn: 17},
			Objective:   "fix the bug",
			Status:      "active",
			Reason:      "why",
			TurnBudget:  10,
			TurnsUsed:   3,
			TokenBudget: 100000,
			TokensUsed:  42000,
		},
		TaskListUpdatedEvent{
			Base: Base{Type: TaskListUpdated, Turn: 18},
			Tasks: []task.Task{
				{Text: "do a", Status: task.StatusInProgress},
				{Text: "do b", Status: task.StatusDone},
			},
		},
	}
}

// TestEventRoundTrip marshals every event type and decodes it back through
// UnmarshalJSON, asserting the decoded value deep-equals the original. This is
// the protocol discipline check: any event that cannot survive a JSON wire
// round trip fails here. Samples are in VALUE form (the shape in-process
// emitters use), so DeepEqual also pins UnmarshalJSON to return values, not
// pointers.
func TestEventRoundTrip(t *testing.T) {
	samples := roundTripSamples()
	if len(samples) != len(eventFactories) {
		t.Fatalf("round-trip samples (%d) out of sync with registered event types (%d)", len(samples), len(eventFactories))
	}
	for _, original := range samples {
		t.Run(string(original.EventType()), func(t *testing.T) {
			data, err := MarshalJSON(original)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			decoded, err := UnmarshalJSON(data)
			if err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if reflect.TypeOf(decoded).Kind() == reflect.Ptr {
				t.Fatalf("UnmarshalJSON must return the value form, got %T", decoded)
			}
			if !reflect.DeepEqual(original, decoded) {
				t.Fatalf("round trip mismatch:\noriginal: %#v\ndecoded:  %#v\nwire: %s", original, decoded, data)
			}
		})
	}
}

// Wire consumers type-switch on the value form, matching in-process
// subscribers; UnmarshalJSON must hand them values.
func TestUnmarshalJSONReturnsValueForm(t *testing.T) {
	data, err := MarshalJSON(AgentStartEvent{Base: Base{Type: AgentStart, Turn: 1}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ev, err := UnmarshalJSON(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	switch ev.(type) {
	case AgentStartEvent:
	case *AgentStartEvent:
		t.Fatal("UnmarshalJSON returned a pointer; value-matching consumers would miss it")
	default:
		t.Fatalf("unexpected decoded type %T", ev)
	}
}

func TestUnmarshalJSONUnknownType(t *testing.T) {
	if _, err := UnmarshalJSON([]byte(`{"type":"nope","turn":1}`)); err == nil {
		t.Fatal("expected error for unknown event type")
	}
	if _, err := UnmarshalJSON([]byte(`{"turn":1}`)); err == nil {
		t.Fatal("expected error for missing event type")
	}
	if _, err := UnmarshalJSON([]byte(`not json`)); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
