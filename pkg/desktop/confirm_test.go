package desktop

import (
	"context"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
)

func TestPermissionResponderPublishesRequest(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	resp := NewPermissionResponder(bus)

	var last events.Event
	bus.Subscribe(func(ctx context.Context, e events.Event) error {
		last = e
		return nil
	})

	info := agent.ToolCallInfo{
		ToolCall: models.ToolCallContent{ID: "c1", Name: "bash"},
		Args:     map[string]any{"command": "ls"},
	}

	go func() {
		time.Sleep(10 * time.Millisecond)
		_ = resp.Submit("c1", true, "once")
	}()

	res, err := resp.ConfirmWithScope(context.Background(), info)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Allow || res.Scope != agent.ScopeOnce {
		t.Fatalf("unexpected result: %+v", res)
	}

	req, ok := last.(events.PermissionRequestEvent)
	if !ok {
		t.Fatalf("expected PermissionRequestEvent, got %T", last)
	}
	if req.ToolName != "bash" {
		t.Fatalf("tool name = %q, want bash", req.ToolName)
	}
}
