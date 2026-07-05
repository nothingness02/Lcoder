# Event Bus Async Dispatch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent slow subscribers (audit log, observability) from blocking the agent main loop while keeping critical subscribers (session persistence) synchronous.

**Architecture:** Extend `events.Bus` with two handler types: `Sync` and `Async`. `Emit` runs sync handlers immediately, then enqueues the event on a bounded channel consumed by a background goroutine. Async handlers run from that goroutine. Provide a drop policy when the async queue is full.

**Tech Stack:** Go 1.25, `pkg/events`.

---

## File Structure

- **Modify:** `pkg/events/bus.go`
- **Modify:** `pkg/events/types.go`
- **Create:** `pkg/events/bus_async_test.go`
- **Modify:** `cmd/lcoder/main.go` and `pkg/tui/app.go` — mark subscribers

---

## Task 1: Add Handler Priority Types

**Files:**
- Modify: `pkg/events/types.go`
- Modify: `pkg/events/bus.go`

- [ ] **Step 1: Update Handler type and add HandlerType**

Modify `pkg/events/types.go`:

```go
// HandlerType classifies how an event handler should be invoked.
type HandlerType int

const (
	HandlerSync HandlerType = iota  // invoked synchronously in Emit
	HandlerAsync                    // invoked from a background goroutine
)

// Handler processes agent events. The zero value is synchronous.
type Handler func(ctx context.Context, event Event) error

// TypedHandler pairs a handler with its invocation mode.
type TypedHandler struct {
	HandlerType HandlerType
	Handler     Handler
}
```

- [ ] **Step 2: Update Bus to support typed handlers**

Modify `pkg/events/bus.go`:

```go
type Bus struct {
	handlers []TypedHandler
	mu       sync.RWMutex

	asyncQueue chan asyncItem
	wg         sync.WaitGroup
	stopOnce   sync.Once
}

type asyncItem struct {
	ctx   context.Context
	event Event
}

// New creates an event bus with a bounded async queue.
func New() *Bus {
	b := &Bus{
		asyncQueue: make(chan asyncItem, 256),
	}
	b.startWorker()
	return b
}
```

- [ ] **Step 3: Implement Subscribe overloads**

```go
// Subscribe registers a synchronous handler.
func (b *Bus) Subscribe(handler Handler) func() {
	return b.SubscribeTyped(TypedHandler{HandlerType: HandlerSync, Handler: handler})
}

// SubscribeAsync registers an asynchronous handler.
func (b *Bus) SubscribeAsync(handler Handler) func() {
	return b.SubscribeTyped(TypedHandler{HandlerType: HandlerAsync, Handler: handler})
}

func (b *Bus) SubscribeTyped(th TypedHandler) func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	idx := len(b.handlers)
	b.handlers = append(b.handlers, th)
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if idx >= len(b.handlers) {
			return
		}
		b.handlers = append(b.handlers[:idx], b.handlers[idx+1:]...)
	}
}
```

- [ ] **Step 4: Implement Emit with sync + async queue**

```go
func (b *Bus) Emit(ctx context.Context, event Event) error {
	b.mu.RLock()
	handlers := make([]TypedHandler, len(b.handlers))
	copy(handlers, b.handlers)
	b.mu.RUnlock()

	var firstErr error
	for _, h := range handlers {
		if h.HandlerType == HandlerSync {
			if err := h.Handler(ctx, event); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("event handler failed for %s: %w", event.EventType(), err)
			}
		}
	}

	for _, h := range handlers {
		if h.HandlerType == HandlerAsync {
			select {
			case b.asyncQueue <- asyncItem{ctx: ctx, event: event}:
			default:
				// Queue full: drop oldest to make room (shed load rather than block).
				select {
				case <-b.asyncQueue:
				default:
				}
				select {
				case b.asyncQueue <- asyncItem{ctx: ctx, event: event}:
				default:
				}
			}
		}
	}
	return firstErr
}
```

Add worker:

```go
func (b *Bus) startWorker() {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		for item := range b.asyncQueue {
			for _, h := range b.handlersSnapshot() {
				if h.HandlerType == HandlerAsync {
					_ = h.Handler(item.ctx, item.event)
				}
			}
		}
	}()
}

func (b *Bus) handlersSnapshot() []TypedHandler {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]TypedHandler, len(b.handlers))
	copy(out, b.handlers)
	return out
}
```

- [ ] **Step 5: Add Close method**

```go
func (b *Bus) Close() {
	b.stopOnce.Do(func() {
		close(b.asyncQueue)
	})
	b.wg.Wait()
}
```

- [ ] **Step 6: Commit**

```bash
git add pkg/events/bus.go pkg/events/types.go
git commit -m "feat(events): sync and async handler dispatch

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 2: Tests for Async Behavior

**Files:**
- Create: `pkg/events/bus_async_test.go`

- [ ] **Step 1: Write test**

```go
package events

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestBus_AsyncHandlerDoesNotBlockSync(t *testing.T) {
	bus := New()
	defer bus.Close()

	var syncCalls int64
	var asyncCalls int64

	bus.Subscribe(func(_ context.Context, _ Event) error {
		atomic.AddInt64(&syncCalls, 1)
		return nil
	})
	bus.SubscribeAsync(func(_ context.Context, _ Event) error {
		time.Sleep(50 * time.Millisecond)
		atomic.AddInt64(&asyncCalls, 1)
		return nil
	})

	start := time.Now()
	_ = bus.Emit(context.Background(), AgentStartEvent{Base: Base{Type: AgentStart, Turn: 0}})
	if time.Since(start) > 10*time.Millisecond {
		t.Fatal("async handler blocked Emit")
	}

	// Wait for async worker.
	time.Sleep(100 * time.Millisecond)
	if atomic.LoadInt64(&syncCalls) != 1 || atomic.LoadInt64(&asyncCalls) != 1 {
		t.Fatalf("expected both handlers called once (sync=%d async=%d)", syncCalls, asyncCalls)
	}
}
```

Run:
```bash
go test ./pkg/events/... -run TestBus_AsyncHandlerDoesNotBlockSync -v
```
Expected: PASS.

- [ ] **Step 2: Commit**

```bash
git add pkg/events/bus_async_test.go
git commit -m "test(events): async handlers do not block Emit

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 3: Mark Subscribers by Priority

**Files:**
- Modify: `cmd/lcoder/main.go`
- Modify: `pkg/tui/app.go`
- Modify: `pkg/observability/collector.go` subscription sites

- [ ] **Step 1: Make session persistence sync, observability/audit async**

Find where each subscriber is registered and choose the type:

```go
// Session persistence — sync (must not lose data)
bus.Subscribe(sessionHandler)

// Observability — async (can tolerate delay)
bus.SubscribeAsync(obsCollector.HandleEvent)

// Audit log — async
bus.SubscribeAsync(auditLogger.HandleEvent)

// TUI — sync (UI updates must be timely, but consider async if it blocks)
bus.Subscribe(tuiHandler)
```

- [ ] **Step 2: Commit**

```bash
git add cmd/lcoder/main.go pkg/tui/app.go pkg/observability/collector.go
git commit -m "refactor: mark event subscribers sync or async

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 4: Full Verification

- [ ] **Step 1: Run events tests**

```bash
go test ./pkg/events/... -count=1
```
Expected: PASS.

- [ ] **Step 2: Build and vet**

```bash
go build ./...
go vet ./pkg/events/...
```
Expected: no output.

---

## Self-review

1. **Spec coverage:**
   - Sync/async handler types: Task 1
   - Async behavior tests: Task 2
   - Subscriber classification: Task 3

2. **Placeholder scan:** No TBD/TODO.

3. **Type consistency:** `HandlerType` constants used in `Bus` and `TypedHandler`.
