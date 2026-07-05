# Decouple TUI from Agent Core Loop Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Provide a synchronous `Runner` interface so the TUI can drive the agent without embedding the agent's blocking goroutine inside a `tea.Cmd`.

**Architecture:** Keep `agent.Runner` as the UI-facing surface. Introduce a small `pkg/tui/runnerqueue.go` that owns a goroutine reading from a prompt queue and calling `agent.Prompt`/`Continue` synchronously. The TUI posts prompts to the queue and receives `RunFinishedMsg` via the event bus. Remove the direct `agent.Prompt` call from `submitPromptCmd`.

**Tech Stack:** Go 1.25, Bubble Tea, `pkg/agent`, `pkg/events`.

---

## File Structure

- **Create:** `pkg/tui/runnerqueue.go`
- **Create:** `pkg/tui/runnerqueue_test.go`
- **Modify:** `pkg/tui/messages.go`
- **Modify:** `pkg/tui/model.go`
- **Modify:** `pkg/tui/keys.go`

---

## Task 1: Create Runner Queue

**Files:**
- Create: `pkg/tui/runnerqueue.go`
- Create: `pkg/tui/runnerqueue_test.go`

- [ ] **Step 1: Write failing test**

Create `pkg/tui/runnerqueue_test.go`:

```go
package tui

import (
	"context"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/models"
)

type queueFakeAgent struct {
	prompts []models.AgentMessage
}

func (f *queueFakeAgent) Prompt(_ context.Context, msg models.AgentMessage) error {
	f.prompts = append(f.prompts, msg)
	return nil
}
func (f *queueFakeAgent) Continue(_ context.Context) error { return nil }

func TestRunnerQueue_DispatchesPrompt(t *testing.T) {
	fa := &queueFakeAgent{}
	q := newRunnerQueue(fa)
	defer q.Stop()

	q.Enqueue(models.UserMessage("hello"))
	time.Sleep(50 * time.Millisecond)

	if len(fa.prompts) != 1 || fa.prompts[0].Text() != "hello" {
		t.Fatalf("expected prompt dispatched, got %v", fa.prompts)
	}
}
```

Run:
```bash
go test ./pkg/tui/ -run TestRunnerQueue_DispatchesPrompt -v
```
Expected: FAIL — `runnerQueue` does not exist.

- [ ] **Step 2: Implement runnerQueue**

Create `pkg/tui/runnerqueue.go`:

```go
package tui

import (
	"context"
	"sync"

	"github.com/lcoder/lcoder/pkg/models"
)

// runnerQueue runs the agent sequentially on a dedicated goroutine so that the
// Bubble Tea update loop stays responsive.
type runnerQueue struct {
	agent  AgentRunner
	queue  chan models.AgentMessage
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newRunnerQueue(agent AgentRunner) *runnerQueue {
	ctx, cancel := context.WithCancel(context.Background())
	rq := &runnerQueue{
		agent:  agent,
		queue:  make(chan models.AgentMessage, 16),
		cancel: cancel,
	}
	rq.wg.Add(1)
	go rq.run(ctx)
	return rq
}

func (q *runnerQueue) Enqueue(msg models.AgentMessage) {
	q.queue <- msg
}

func (q *runnerQueue) Stop() {
	q.cancel()
	close(q.queue)
	q.wg.Wait()
}

func (q *runnerQueue) run(ctx context.Context) {
	defer q.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-q.queue:
			if !ok {
				return
			}
			_ = q.agent.Prompt(ctx, msg)
		}
	}
}
```

Run:
```bash
go test ./pkg/tui/ -run TestRunnerQueue_DispatchesPrompt -v
```
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/tui/runnerqueue.go pkg/tui/runnerqueue_test.go
git commit -m "feat(tui): runner queue decouples agent from tea.Cmd

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 2: Wire Runner Queue into Model

**Files:**
- Modify: `pkg/tui/model.go`
- Modify: `pkg/tui/messages.go`
- Modify: `pkg/tui/keys.go`

- [ ] **Step 1: Add runnerQueue to Model**

Modify `pkg/tui/model.go` `Model` struct:

```go
type Model struct {
	// ... existing fields ...
	runnerQueue *runnerQueue
}
```

In `NewModel`, create:

```go
m.runnerQueue = newRunnerQueue(agent)
```

- [ ] **Step 2: Replace submitPromptCmd**

Modify `pkg/tui/messages.go`:

```go
// submitPromptCmd enqueues the user message for the runner queue.
func submitPromptCmd(queue *runnerQueue, sess SessionWriter, text string) tea.Cmd {
	return func() tea.Msg {
		msg := models.UserMessage(text)
		if err := sess.Append(msg); err != nil {
			return AgentDoneMsg{Err: err}
		}
		queue.Enqueue(msg)
		return SendPromptMsg{Text: text}
	}
}
```

Update call sites in `keys.go`:

```go
submitPromptCmd(m.runnerQueue, m.session, expandHomeMentions(text))
```

- [ ] **Step 3: Handle run completion via event bus**

TUI already subscribes to events. Add handling for `AgentEndEvent` in the model's event handler to clear the spinner and show `AgentDoneMsg{}`.

Modify `pkg/tui/model.go` where events are processed:

```go
case events.AgentEnd:
	m.spinnerRunning = false
	return m, func() tea.Msg { return AgentDoneMsg{} }
```

- [ ] **Step 4: Run TUI tests**

```bash
go test ./pkg/tui/... -count=1
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/tui/model.go pkg/tui/messages.go pkg/tui/keys.go
git commit -m "refactor(tui): drive agent via runner queue instead of blocking tea.Cmd

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 3: Full Verification

- [ ] **Step 1: Run TUI tests**

```bash
go test ./pkg/tui/... -count=1
```
Expected: PASS.

- [ ] **Step 2: Build and vet**

```bash
go build ./...
go vet ./pkg/tui/...
```
Expected: no output.

---

## Self-review

1. **Spec coverage:**
   - Runner queue: Task 1
   - TUI wiring: Task 2

2. **Placeholder scan:** No TBD/TODO.

3. **Type consistency:** `AgentRunner` interface unchanged.
