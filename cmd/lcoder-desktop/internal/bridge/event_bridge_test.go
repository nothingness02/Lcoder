package bridge

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
)

func TestEventBridgeMapsPermissionRequest(t *testing.T) {
	bridge := NewEventBridge(context.Background(), nil)
	payload, err := bridge.mapEvent(events.PermissionRequestEvent{
		Base:       events.Base{Type: events.PermissionRequest},
		RequestID:  "r1",
		ToolCallID: "c1",
		ToolName:   "bash",
		Args:       map[string]any{"command": "ls"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if payload == nil || payload.Type != "permission:request" {
		t.Fatalf("expected permission:request, got %+v", payload)
	}
}

func TestEventBridgeMapsMessageUpdate(t *testing.T) {
	bridge := NewEventBridge(context.Background(), nil)
	m := models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: ""})
	m.ID = "a1"
	payload, err := bridge.mapEvent(events.MessageUpdateEvent{
		Base:       events.Base{Type: events.MessageUpdate},
		Delta:      "hello",
		IsThinking: false,
		Message:    m,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if payload == nil || payload.Type != "message:delta" {
		t.Fatalf("expected message:delta, got %+v", payload)
	}
}
