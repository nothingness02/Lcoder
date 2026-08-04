package events

import (
	"context"
	"testing"
)

func TestBusOrderAndUnsubscribe(t *testing.T) {
	bus := New()

	var order []string
	h1 := bus.Subscribe(func(ctx context.Context, ev Event) error {
		order = append(order, "h1:"+string(ev.EventType()))
		return nil
	})
	_ = bus.Subscribe(func(ctx context.Context, ev Event) error {
		order = append(order, "h2:"+string(ev.EventType()))
		return nil
	})

	ctx := context.Background()
	if err := bus.Emit(ctx, AgentStartEvent{Base: Base{Type: AgentStart}}); err != nil {
		t.Fatalf("emit: %v", err)
	}

	if len(order) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(order))
	}
	if order[0] != "h1:agent_start" || order[1] != "h2:agent_start" {
		t.Fatalf("unexpected order: %v", order)
	}

	h1()
	order = nil
	if err := bus.Emit(ctx, AgentStartEvent{Base: Base{Type: AgentStart}}); err != nil {
		t.Fatalf("emit after unsubscribe: %v", err)
	}
	if len(order) != 1 || order[0] != "h2:agent_start" {
		t.Fatalf("expected only h2, got %v", order)
	}
}

// Unsubscribing out of registration order must remove exactly the target
// handler, not whatever happens to sit at its old index.
func TestBusNonLIFOUnsubscribe(t *testing.T) {
	bus := New()

	var calls []string
	mk := func(name string) Handler {
		return func(ctx context.Context, ev Event) error {
			calls = append(calls, name)
			return nil
		}
	}
	u1 := bus.Subscribe(mk("h1"))
	u2 := bus.Subscribe(mk("h2"))
	u3 := bus.Subscribe(mk("h3"))

	// Remove the middle subscription first (non-LIFO).
	u2()
	ctx := context.Background()
	if err := bus.Emit(ctx, AgentStartEvent{Base: Base{Type: AgentStart}}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if len(calls) != 2 || calls[0] != "h1" || calls[1] != "h3" {
		t.Fatalf("expected h1,h3 after middle unsubscribe, got %v", calls)
	}

	// Double-unsubscribe is a no-op; removing the rest leaves the bus empty.
	u2()
	u1()
	u3()
	calls = nil
	if err := bus.Emit(ctx, AgentStartEvent{Base: Base{Type: AgentStart}}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("expected no handlers left, got %v", calls)
	}

	// A fresh subscription still works after the churn.
	bus.Subscribe(mk("h4"))
	if err := bus.Emit(ctx, AgentStartEvent{Base: Base{Type: AgentStart}}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if len(calls) != 1 || calls[0] != "h4" {
		t.Fatalf("expected only h4, got %v", calls)
	}
}
