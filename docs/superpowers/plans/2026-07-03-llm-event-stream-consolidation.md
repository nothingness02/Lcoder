# Consolidate LLM Event Stream Abstractions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the redundant `GatewayEvent` wrapper and let `pkg/llm/client.go` expose `provider.Event` directly to consumers, with helper functions for common extractions.

**Architecture:** Delete `GatewayEvent` type. Change `Client.StreamTurn` to return a stream of `provider.Event`. Move `Usage()`, `FinalMessage()`, and `Error()` helpers to package-level functions on `provider.Event`. Update `streamer.go` and any tests to consume `provider.Event`.

**Tech Stack:** Go 1.25, `pkg/llm`, `pkg/llm/provider`.

---

## File Structure

- **Modify:** `pkg/llm/client.go`
- **Modify:** `pkg/llm/stream.go` (if exists)
- **Modify:** `pkg/llm/provider/event.go`
- **Modify:** `pkg/agent/streamer.go`
- **Modify:** `pkg/llm/client_test.go` and related tests

---

## Task 1: Add Helper Functions to provider.Event

**Files:**
- Modify: `pkg/llm/provider/event.go`

- [ ] **Step 1: Add helper methods**

Append to `pkg/llm/provider/event.go`:

```go
// Usage returns LLM usage from a done event if present.
func (e Event) Usage() (models.LLMUsage, bool) {
	if e.Kind != KindDone || e.Usage == nil {
		return models.LLMUsage{}, false
	}
	return *e.Usage, true
}

// FinalMessage returns the final assistant message from a done event.
func (e Event) FinalMessage() (models.AgentMessage, error) {
	if e.Kind != KindDone {
		return models.AgentMessage{}, fmt.Errorf("final message only available on done events")
	}
	return e.Message, nil
}

// Error returns a classified error from an error event.
func (e Event) Error() (EventError, bool) {
	if e.Kind != KindError || e.Err == nil {
		return EventError{}, false
	}
	return *e.Err, true
}
```

- [ ] **Step 2: Commit**

```bash
git add pkg/llm/provider/event.go
git commit -m "feat(provider): helper accessors on Event

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 2: Replace GatewayEvent with provider.Event

**Files:**
- Modify: `pkg/llm/client.go`
- Modify: `pkg/agent/streamer.go`
- Modify: related tests

- [ ] **Step 1: Update Client.StreamTurn**

Replace `pkg/llm/client.go` content:

```go
// pkg/llm/client.go
package llm

import (
	"context"

	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/llm/engine"
	"github.com/lcoder/lcoder/pkg/llm/provider"
	"github.com/lcoder/lcoder/pkg/models"
)

// Client is the in-process LLM client.
type Client struct {
	engine *engine.Engine
}

func NewClient(eng *engine.Engine) *Client {
	return &Client{engine: eng}
}

// StreamTurn starts a provider turn and returns a channel of provider events.
func (c *Client) StreamTurn(ctx context.Context, req models.TurnRequest) (*TurnStream, error) {
	src, err := c.engine.StreamTurn(ctx, req)
	if err != nil {
		return nil, err
	}
	out := make(chan provider.Event)
	go func() {
		defer close(out)
		for ev := range src {
			out <- ev
		}
	}()
	return &TurnStream{ch: out}, nil
}

// RegisterProvider stores a provider connection on the engine.
func (c *Client) RegisterProvider(ctx context.Context, name string, conn config.ProviderConn) error {
	c.engine.RegisterProvider(name, provider.Conn{...})
	return nil
}

// ListModels, ModelWindow, ModelMaxOutput, Health unchanged.

// TurnStream is a channel-backed stream of provider events.
type TurnStream struct {
	ch <-chan provider.Event
}

// Next returns the next event or false when closed.
func (s *TurnStream) Next() (provider.Event, bool) {
	ev, ok := <-s.ch
	return ev, ok
}
```

- [ ] **Step 2: Update streamer.go**

Modify `pkg/agent/streamer.go` to consume `provider.Event` instead of `GatewayEvent`. Use `ev.Kind`, `ev.Usage()`, `ev.FinalMessage()`, `ev.Error()`.

- [ ] **Step 3: Update tests**

Find all references to `GatewayEvent` and `llm.GatewayEvent` and replace with `provider.Event`.

Run:
```bash
go test ./pkg/llm/... ./pkg/agent/... -count=1
```
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/llm/client.go pkg/agent/streamer.go pkg/llm/*_test.go
git commit -m "refactor(llm): remove GatewayEvent wrapper, use provider.Event directly

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 3: Full Verification

- [ ] **Step 1: Run tests**

```bash
go test ./pkg/llm/... ./pkg/agent/... -count=1
```
Expected: PASS.

- [ ] **Step 2: Build and vet**

```bash
go build ./...
go vet ./pkg/llm/... ./pkg/agent/...
```
Expected: no output.

---

## Self-review

1. **Spec coverage:**
   - Event helpers: Task 1
   - GatewayEvent removal: Task 2

2. **Placeholder scan:** No TBD/TODO.

3. **Type consistency:** `TurnStream.Next()` returns `provider.Event`.
