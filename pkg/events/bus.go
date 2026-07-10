package events

import (
	"context"
	"errors"
	"sync"
)

// AsyncOptions configures an asynchronous event handler.
type AsyncOptions struct {
	// BufferSize is the maximum number of events queued for the handler.
	// When the buffer is full, the oldest event is dropped. Defaults to 64.
	BufferSize int
}

// Handler processes agent events.
type Handler func(ctx context.Context, event Event) error

// Bus broadcasts events to registered handlers.
type Bus struct {
	handlers []Handler
	async    []*asyncHandler
	mu       sync.RWMutex
	closed   bool
}

// New creates an event bus.
func New() *Bus {
	return &Bus{}
}

// asyncHandler wraps a handler that runs in its own goroutine with a bounded
// queue. The queue drops the oldest event when full.
type asyncHandler struct {
	handler Handler
	ch      chan Event
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	sendMu  sync.Mutex
	close   sync.Once
}

// Subscribe registers a synchronous handler. The returned function unsubscribes it.
func (b *Bus) Subscribe(handler Handler) func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return func() {}
	}
	idx := len(b.handlers)
	b.handlers = append(b.handlers, handler)
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if idx >= len(b.handlers) {
			return
		}
		b.handlers = append(b.handlers[:idx], b.handlers[idx+1:]...)
	}
}

// SubscribeAsync registers an asynchronous handler that runs in its own
// goroutine. Events are queued; if the queue fills up the oldest event is
// dropped. The returned function unsubscribes and stops the worker.
func (b *Bus) SubscribeAsync(handler Handler, opts AsyncOptions) func() {
	if opts.BufferSize <= 0 {
		opts.BufferSize = 64
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &asyncHandler{
		handler: handler,
		ch:      make(chan Event, opts.BufferSize),
		cancel:  cancel,
	}

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		for ev := range a.ch {
			_ = a.handler(ctx, ev)
		}
	}()

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		a.stop()
		return func() {}
	}
	b.async = append(b.async, a)
	b.mu.Unlock()

	return func() { b.unsubscribeAsync(a) }
}

func (b *Bus) unsubscribeAsync(a *asyncHandler) {
	a.cancel()

	b.mu.Lock()
	for i, x := range b.async {
		if x == a {
			b.async = append(b.async[:i], b.async[i+1:]...)
			break
		}
	}
	b.mu.Unlock()

	a.stop()
	a.wg.Wait()
}

func (a *asyncHandler) stop() {
	a.close.Do(func() {
		a.sendMu.Lock()
		close(a.ch)
		a.sendMu.Unlock()
	})
}

// enqueue adds an event to the handler's queue, dropping the oldest event if
// the queue is full.
func (a *asyncHandler) enqueue(ev Event) {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	select {
	case a.ch <- ev:
	default:
		// Drop the oldest event to make room.
		select {
		case <-a.ch:
		default:
		}
		a.ch <- ev
	}
}

// Emit synchronously dispatches an event to all handlers.
// Synchronous handlers are invoked in registration order. If a handler returns
// an error, subsequent handlers still run, and the first error is returned.
// Asynchronous handlers receive the event via their queue.
func (b *Bus) Emit(ctx context.Context, event Event) error {
	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		return errors.New("event bus is closed")
	}
	handlers := make([]Handler, len(b.handlers))
	copy(handlers, b.handlers)
	async := make([]*asyncHandler, len(b.async))
	copy(async, b.async)
	b.mu.RUnlock()

	var firstErr error
	for _, h := range handlers {
		if err := h(ctx, event); err != nil && firstErr == nil {
			firstErr = errors.New("event handler failed for " + string(event.EventType()) + ": " + err.Error())
		}
	}
	for _, a := range async {
		a.enqueue(event)
	}
	return firstErr
}

// Close drains all asynchronous handler queues and stops their workers.
// After Close, Emit returns an error. Synchronous handlers are not invoked again.
func (b *Bus) Close() error {
	b.mu.Lock()
	b.closed = true
	async := make([]*asyncHandler, len(b.async))
	copy(async, b.async)
	b.async = b.async[:0]
	b.mu.Unlock()

	var wg sync.WaitGroup
	for _, a := range async {
		a.stop()
		wg.Add(1)
		go func(a *asyncHandler) {
			defer wg.Done()
			a.wg.Wait()
		}(a)
	}
	wg.Wait()
	return nil
}
