package desktop

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/events"
)

// PermissionResponder implements agent.UserConfirmation by publishing a
// permission_request event and waiting for the frontend to call Submit.
type PermissionResponder struct {
	bus     *events.Bus
	mu      sync.Mutex
	pending map[string]chan agent.ConfirmResult
}

func NewPermissionResponder(bus *events.Bus) *PermissionResponder {
	return &PermissionResponder{
		bus:     bus,
		pending: make(map[string]chan agent.ConfirmResult),
	}
}

func (r *PermissionResponder) Confirm(ctx context.Context, info agent.ToolCallInfo) (bool, error) {
	res, err := r.ConfirmWithScope(ctx, info)
	return res.Allow, err
}

func (r *PermissionResponder) ConfirmWithScope(ctx context.Context, info agent.ToolCallInfo) (agent.ConfirmResult, error) {
	id := info.ToolCall.ID
	if id == "" {
		id = uuid.New().String()
	}
	ch := make(chan agent.ConfirmResult, 1)

	r.mu.Lock()
	r.pending[id] = ch
	r.mu.Unlock()

	_ = r.bus.Emit(ctx, events.PermissionRequestEvent{
		Base:       events.Base{Type: events.PermissionRequest},
		RequestID:  id,
		ToolCallID: id,
		ToolName:   info.ToolCall.Name,
		Args:       info.Args,
	})

	select {
	case <-ctx.Done():
		r.mu.Lock()
		delete(r.pending, id)
		r.mu.Unlock()
		return agent.ConfirmResult{Allow: false, Scope: agent.ScopeDeny}, ctx.Err()
	case res := <-ch:
		return res, nil
	}
}

func (r *PermissionResponder) Submit(id string, allow bool, scope string) error {
	r.mu.Lock()
	ch, ok := r.pending[id]
	if ok {
		delete(r.pending, id)
	}
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("permission request %s not found", id)
	}

	parsed := agent.ScopeOnce
	switch scope {
	case "once":
		parsed = agent.ScopeOnce
	case "project":
		parsed = agent.ScopeProject
	case "global":
		parsed = agent.ScopeGlobal
	default:
		parsed = agent.ScopeDeny
	}
	ch <- agent.ConfirmResult{Allow: allow && parsed != agent.ScopeDeny, Scope: parsed}
	return nil
}
