package bridge

import (
	"context"

	desktop "github.com/lcoder/lcoder/pkg/desktop"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// EventBridge subscribes to the agent event bus and forwards them to the
// Wails frontend via runtime.EventsEmit.
type EventBridge struct {
	ctx   context.Context
	bus   *events.Bus
	unsub func()
}

func NewEventBridge(ctx context.Context, bus *events.Bus) *EventBridge {
	return &EventBridge{ctx: ctx, bus: bus}
}

func (b *EventBridge) Start() {
	b.unsub = b.bus.Subscribe(b.onEvent)
}

func (b *EventBridge) Stop() {
	if b.unsub != nil {
		b.unsub()
	}
}

type frontendEvent struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

func (b *EventBridge) onEvent(ctx context.Context, ev events.Event) error {
	payload, err := b.mapEvent(ev)
	if err != nil {
		return err
	}
	if payload != nil {
		runtime.EventsEmit(b.ctx, payload.Type, payload.Data)
	}
	return nil
}

func (b *EventBridge) mapEvent(ev events.Event) (*frontendEvent, error) {
	switch e := ev.(type) {
	case events.MessageStartEvent:
		return &frontendEvent{Type: "message:start", Data: desktop.MessageToUI(e.Message)}, nil
	case events.MessageUpdateEvent:
		return &frontendEvent{Type: "message:delta", Data: map[string]any{
			"id":          e.Message.ID,
			"delta":       e.Delta,
			"is_thinking": e.IsThinking,
		}}, nil
	case events.MessageEndEvent:
		return &frontendEvent{Type: "message:end", Data: desktop.MessageToUI(e.Message)}, nil
	case events.ToolExecutionStartEvent:
		return &frontendEvent{Type: "tool:start", Data: map[string]any{
			"id":   e.ToolCallID,
			"name": e.ToolName,
			"args": e.Args,
		}}, nil
	case events.ToolExecutionEndEvent:
		return &frontendEvent{Type: "tool:end", Data: desktop.UIToolResult{
			ToolCallID: e.ToolCallID,
			Name:       e.ToolName,
			Output:     e.Result.Text(),
			IsError:    e.IsError,
		}}, nil
	case events.TurnStartEvent:
		return &frontendEvent{Type: "turn:start", Data: map[string]any{"turn": e.Turn}}, nil
	case events.TurnEndEvent:
		return &frontendEvent{Type: "turn:end", Data: map[string]any{"turn": e.Turn}}, nil
	case events.SessionLoadedEvent:
		return &frontendEvent{Type: "session:loaded", Data: map[string]any{
			"session_id": e.SessionID,
			"messages":   desktop.MessagesToUI(e.Messages),
		}}, nil
	case events.PermissionRequestEvent:
		return &frontendEvent{Type: "permission:request", Data: desktop.PermissionRequest{
			ID:       e.RequestID,
			ToolName: e.ToolName,
			Args:     e.Args,
		}}, nil
	case events.ErrorEvent:
		return &frontendEvent{Type: "app:error", Data: map[string]any{"message": e.Message}}, nil
	case events.CompactionStartedEvent:
		return &frontendEvent{Type: "status:compacting", Data: map[string]any{"compacting": true}}, nil
	case events.CompactionCommittedEvent:
		return &frontendEvent{Type: "status:compacting", Data: map[string]any{"compacting": false}}, nil
	default:
		return nil, nil
	}
}
