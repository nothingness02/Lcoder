package events

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testEvent struct{ Base }

func TestBus_AsyncHandlerReceivesEvents(t *testing.T) {
	bus := New()
	var count int32
	unsub := bus.SubscribeAsync(func(ctx context.Context, ev Event) error {
		atomic.AddInt32(&count, 1)
		return nil
	}, AsyncOptions{BufferSize: 4})
	defer unsub()

	_ = bus.Emit(context.Background(), testEvent{Base{Type: AgentStart}})
	_ = bus.Emit(context.Background(), testEvent{Base{Type: AgentEnd}})

	// Wait for the worker to process both events.
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&count); got != 2 {
		t.Fatalf("async handler processed %d events, want 2", got)
	}
}

func TestBus_AsyncHandlerDoesNotBlockEmit(t *testing.T) {
	bus := New()
	blocker := make(chan struct{})
	var processed int32

	unsub := bus.SubscribeAsync(func(ctx context.Context, ev Event) error {
		<-blocker
		atomic.AddInt32(&processed, 1)
		return nil
	}, AsyncOptions{BufferSize: 4})
	defer unsub()

	// Emit should return immediately even though the handler is blocked.
	start := time.Now()
	_ = bus.Emit(context.Background(), testEvent{Base{Type: AgentStart}})
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("emit took %v, expected non-blocking", elapsed)
	}

	close(blocker)
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&processed) != 1 {
		t.Fatal("handler did not process event after unblock")
	}
}

func TestBus_AsyncHandlerDropsOldestWhenFull(t *testing.T) {
	bus := New()
	blocker := make(chan struct{})
	var mu sync.Mutex
	var received []EventType

	unsub := bus.SubscribeAsync(func(ctx context.Context, ev Event) error {
		<-blocker
		mu.Lock()
		received = append(received, ev.EventType())
		mu.Unlock()
		return nil
	}, AsyncOptions{BufferSize: 2})
	defer unsub()

	// Fill the buffer while handler is blocked, then emit one more.
	_ = bus.Emit(context.Background(), testEvent{Base{Type: AgentStart}})
	_ = bus.Emit(context.Background(), testEvent{Base{Type: TurnStart}})
	_ = bus.Emit(context.Background(), testEvent{Base{Type: TurnEnd}}) // drops AgentStart

	close(blocker)
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 2 {
		t.Fatalf("received %d events, want 2", len(received))
	}
	if received[0] != TurnStart {
		t.Fatalf("first received = %v, want turn_start", received[0])
	}
	if received[1] != TurnEnd {
		t.Fatalf("second received = %v, want turn_end", received[1])
	}
}

func TestBus_CloseDrainsAsyncHandlers(t *testing.T) {
	bus := New()
	var count int32
	unsub := bus.SubscribeAsync(func(ctx context.Context, ev Event) error {
		atomic.AddInt32(&count, 1)
		return nil
	}, AsyncOptions{BufferSize: 4})
	defer unsub()

	_ = bus.Emit(context.Background(), testEvent{Base{Type: AgentStart}})
	if err := bus.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := atomic.LoadInt32(&count); got != 1 {
		t.Fatalf("drained %d events, want 1", got)
	}

	// Emit after Close returns an error.
	if err := bus.Emit(context.Background(), testEvent{Base{Type: AgentStart}}); err == nil {
		t.Fatal("expected error after close")
	}
}
