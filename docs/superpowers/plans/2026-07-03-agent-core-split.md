# Split Agent Core Responsibilities Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reduce the responsibilities of the top-level `Agent` type so it is only a coordinator. Move checkpoint scheduling, reminder coordination, and compaction triggering into focused sub-components.

**Architecture:** Create `pkg/agent/checkpointmgr.go` to own automatic checkpoint decisions; create `pkg/agent/remindercoord.go` to own ephemeral reminder production; keep `Agent.run()` as a thin orchestrator that calls these components. The existing `stateHolder`, `streamer`, and `executor` remain separate.

**Tech Stack:** Go 1.25, existing `pkg/agent`, `pkg/checkpoint`, `pkg/contextmgr`, `pkg/events`.

---

## File Structure

- **Create:** `pkg/agent/checkpointmgr.go`
- **Create:** `pkg/agent/checkpointmgr_test.go`
- **Create:** `pkg/agent/remindercoord.go`
- **Create:** `pkg/agent/remindercoord_test.go`
- **Modify:** `pkg/agent/agent.go` — add sub-component fields
- **Modify:** `pkg/agent/loop.go` — delegate to sub-components
- **Modify:** `pkg/agent/config.go` — add sub-component config hooks

---

## Task 1: Extract CheckpointManager

**Files:**
- Create: `pkg/agent/checkpointmgr.go`
- Create: `pkg/agent/checkpointmgr_test.go`
- Modify: `pkg/agent/agent.go`
- Modify: `pkg/agent/loop.go`

- [ ] **Step 1: Write failing test**

Create `pkg/agent/checkpointmgr_test.go`:

```go
package agent

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/checkpoint"
	"github.com/lcoder/lcoder/pkg/models"
)

type fakeCheckpointSource struct {
	cp *checkpoint.Checkpoint
}

func (f *fakeCheckpointSource) Checkpoint() (*checkpoint.Checkpoint, error) {
	return f.cp, nil
}

func TestCheckpointManager_SkipsByInterval(t *testing.T) {
	store := checkpoint.NewFileStore(t.TempDir())
	cp := &checkpoint.Checkpoint{
		Runtime: &checkpoint.RuntimeSnapshot{Turn: 1},
	}
	mgr := newCheckpointManager("sess", store, &fakeCheckpointSource{cp: cp}, 3)

	mgr.MaybeSave(context.Background(), 1, checkpoint.ReasonAuto)
	mgr.MaybeSave(context.Background(), 2, checkpoint.ReasonAuto)
	ids, _ := store.List("sess")
	if len(ids) != 0 {
		t.Fatalf("expected 0 saves before interval, got %d", len(ids))
	}

	mgr.MaybeSave(context.Background(), 3, checkpoint.ReasonAuto)
	ids, _ = store.List("sess")
	if len(ids) != 1 {
		t.Fatalf("expected 1 save at interval, got %d", len(ids))
	}
}

func TestCheckpointManager_AlwaysSavesCrash(t *testing.T) {
	store := checkpoint.NewFileStore(t.TempDir())
	cp := &checkpoint.Checkpoint{}
	mgr := newCheckpointManager("sess", store, &fakeCheckpointSource{cp: cp}, 5)
	mgr.MaybeSave(context.Background(), 2, checkpoint.ReasonCrash)
	ids, _ := store.List("sess")
	if len(ids) != 1 {
		t.Fatalf("expected crash checkpoint saved immediately, got %d", len(ids))
	}
}
```

Run:
```bash
go test ./pkg/agent/... -run TestCheckpointManager -v
```
Expected: FAIL — `checkpointManager` does not exist.

- [ ] **Step 2: Implement CheckpointManager**

Create `pkg/agent/checkpointmgr.go`:

```go
package agent

import (
	"context"
	"fmt"

	"github.com/lcoder/lcoder/pkg/checkpoint"
	"github.com/lcoder/lcoder/pkg/events"
)

// checkpointManager decides when to persist an automatic checkpoint.
type checkpointManager struct {
	sessionID string
	store     checkpoint.Store
	source    checkpoint.Source
	interval  int
	emitter   *eventEmitter
}

func newCheckpointManager(sessionID string, store checkpoint.Store, source checkpoint.Source, interval int) *checkpointManager {
	return &checkpointManager{
		sessionID: sessionID,
		store:     store,
		source:    source,
		interval:  interval,
	}
}

func (c *checkpointManager) MaybeSave(ctx context.Context, turn int, reason string) {
	if c.store == nil || c.sessionID == "" {
		return
	}
	interval := c.interval
	if interval <= 0 {
		interval = 1
	}
	if reason == checkpoint.ReasonAuto && turn%interval != 0 {
		return
	}
	cp, err := c.source.Checkpoint()
	if err != nil {
		c.emitError(ctx, turn, "checkpoint: "+err.Error())
		return
	}
	if cp.Session != nil {
		cp.Session.Reason = reason
	}
	if err := c.store.Save(c.sessionID, cp); err != nil {
		c.emitError(ctx, turn, "checkpoint save: "+err.Error())
	}
}

func (c *checkpointManager) emitError(ctx context.Context, turn int, msg string) {
	if c.emitter == nil {
		return
	}
	c.emitter.emit(ctx, events.ErrorEvent{Base: events.Base{Type: events.Error, Turn: turn}, Message: msg})
}
```

- [ ] **Step 3: Wire into Agent**

Modify `pkg/agent/agent.go` `Agent` struct:

```go
type Agent struct {
	cfg          Config
	mgr          *contextmgr.Manager
	llm          *llm.Client
	registry     *tools.Registry
	bus          *events.Bus
	obsCollector *observability.Collector
	emitter      *eventEmitter

	loopState      *stateHolder
	streamer       *streamer
	executor       *executor
	checkpointMgr  *checkpointManager
	reminderCoord  *reminderCoordinator
}
```

Modify `New` in `pkg/agent/agent.go`:

```go
ag.checkpointMgr = newCheckpointManager(cfg.SessionID, cfg.CheckpointStore, ag, cfg.CheckpointInterval)
ag.checkpointMgr.emitter = ag.emitter
ag.reminderCoord = newReminderCoordinator(ag.mgr, cfg.ReminderProducers)
```

Make `Agent` implement `checkpoint.Source` by keeping the existing `Checkpoint()` method.

Modify `pkg/agent/loop.go`:

```go
// replace the existing maybeCheckpoint call with:
a.checkpointMgr.MaybeSave(ctx, turn, checkpoint.ReasonAuto)
```

Remove the old `maybeCheckpoint` method from `loop.go`.

Run:
```bash
go test ./pkg/agent/... -run TestCheckpointManager -v
```
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/agent/checkpointmgr.go pkg/agent/checkpointmgr_test.go pkg/agent/agent.go pkg/agent/loop.go
git commit -m "refactor(agent): extract checkpoint manager

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 2: Extract ReminderCoordinator

**Files:**
- Create: `pkg/agent/remindercoord.go`
- Create: `pkg/agent/remindercoord_test.go`
- Modify: `pkg/agent/agent.go`
- Modify: `pkg/agent/loop.go`

- [ ] **Step 1: Write failing test**

Create `pkg/agent/remindercoord_test.go`:

```go
package agent

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/models"
)

func TestReminderCoordinator_Refresh(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 1000})
	producer := func(msgs []models.AgentMessage) []string {
		return []string{"remember X"}
	}
	coord := newReminderCoordinator(mgr, []ReminderProducer{producer})
	coord.Refresh()
	if len(mgr.EphemeralReminders()) != 1 {
		t.Fatalf("expected 1 reminder, got %d", len(mgr.EphemeralReminders()))
	}
}
```

Run:
```bash
go test ./pkg/agent/... -run TestReminderCoordinator -v
```
Expected: FAIL — `reminderCoordinator` does not exist.

- [ ] **Step 2: Implement ReminderCoordinator**

Create `pkg/agent/remindercoord.go`:

```go
package agent

import "github.com/lcoder/lcoder/pkg/contextmgr"

// reminderCoordinator owns ephemeral reminder production.
type reminderCoordinator struct {
	mgr       *contextmgr.Manager
	producers []ReminderProducer
}

func newReminderCoordinator(mgr *contextmgr.Manager, producers []ReminderProducer) *reminderCoordinator {
	return &reminderCoordinator{mgr: mgr, producers: producers}
}

// Refresh clears existing reminders, runs all producers over the current
// conversation, and stages the results for the next turn.
func (r *reminderCoordinator) Refresh() {
	r.mgr.ClearEphemeralReminders()
	if len(r.producers) == 0 {
		return
	}
	msgs := r.mgr.AllMessages()
	var all []string
	for _, p := range r.producers {
		all = append(all, p(msgs)...)
	}
	r.mgr.SetEphemeralReminders(all)
}
```

- [ ] **Step 3: Wire into Agent and loop**

Modify `pkg/agent/loop.go`:

```go
// replace:
// a.refreshEphemeralReminders()
// with:
a.reminderCoord.Refresh()
```

Remove the old `refreshEphemeralReminders` method from `loop.go`.

Run:
```bash
go test ./pkg/agent/... -run TestReminderCoordinator -v
```
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/agent/remindercoord.go pkg/agent/remindercoord_test.go pkg/agent/agent.go pkg/agent/loop.go
git commit -m "refactor(agent): extract reminder coordinator

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 3: Clean Up Agent.run Orchestration

**Files:**
- Modify: `pkg/agent/loop.go`

- [ ] **Step 1: Refactor run() to read like an orchestrator**

Modify `pkg/agent/loop.go` `run()` so it looks like:

```go
func (a *Agent) run(ctx context.Context, initialPrompts []models.AgentMessage) error {
	a.loopState.SetState(StateStreaming)
	a.loopState.ResetAbort()

	turn := a.loopState.StartRun()
	for _, msg := range initialPrompts {
		a.appendMessage(msg)
	}

	a.emit(ctx, events.AgentStartEvent{Base: events.Base{Type: events.AgentStart, Turn: turn}})

	for {
		a.drainSteeringQueue()
		a.emit(ctx, events.TurnStartEvent{Base: events.Base{Type: events.TurnStart, Turn: turn}})

		a.reminderCoord.Refresh()
		a.maybeCompact(ctx, turn)

		_, tools, modelRef, execMode := a.applyMode()

		assistantMsg, err := a.streamer.stream(ctx, turn, modelRef, tools, a.loopState.SetStreamAbort, a.loopState.ClearStreamAbort)
		if err != nil {
			a.emit(ctx, events.ErrorEvent{Base: events.Base{Type: events.Error, Turn: turn}, Message: err.Error()})
			break
		}
		a.appendMessage(assistantMsg)

		toolCalls := assistantMsg.ToolCalls()
		var toolResults []models.AgentMessage
		terminate := false
		if len(toolCalls) > 0 {
			a.loopState.SetState(StateExecutingTools)
			toolResults, terminate = a.executor.execute(ctx, turn, assistantMsg, toolCalls, execMode)
			for _, r := range toolResults {
				a.appendMessage(r)
			}
			a.loopState.SetState(StateStreaming)
		}

		a.emit(ctx, events.TurnEndEvent{Base: events.Base{Type: events.TurnEnd, Turn: turn}, Message: assistantMsg, ToolResults: toolResults})

		turn++
		a.loopState.SetTurn(turn)
		a.checkpointMgr.MaybeSave(ctx, turn, checkpoint.ReasonAuto)

		if a.maxTurnsReached(turn) {
			break
		}
		if terminate {
			break
		}
		if a.shouldStop(ctx, assistantMsg, toolResults) {
			if !a.drainFollowUpQueue() {
				break
			}
		}
	}

	a.emit(ctx, events.AgentEndEvent{Base: events.Base{Type: events.AgentEnd, Turn: turn}, Messages: a.mgr.AllMessages()})
	a.loopState.SetState(StateIdle)
	return nil
}
```

Add helper methods:

```go
func (a *Agent) drainSteeringQueue() {
	pending := a.loopState.DrainSteeringQueue()
	for _, msg := range pending {
		a.appendMessage(msg)
	}
}

func (a *Agent) drainFollowUpQueue() bool {
	followUps := a.loopState.DrainFollowUpQueue()
	for _, msg := range followUps {
		a.appendMessage(msg)
	}
	return len(followUps) > 0
}
```

- [ ] **Step 2: Run agent tests**

```bash
go test ./pkg/agent/... -count=1
```
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/agent/loop.go
git commit -m "refactor(agent): run() reads as coordinator only

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 4: Full Verification

- [ ] **Step 1: Run all affected tests**

```bash
go test ./pkg/agent/... -count=1
```
Expected: PASS.

- [ ] **Step 2: Build and vet**

```bash
go build ./...
go vet ./pkg/agent/...
```
Expected: no output.

---

## Self-review

1. **Spec coverage:**
   - Checkpoint scheduling extracted: Task 1
   - Reminder coordination extracted: Task 2
   - `Agent.run()` as orchestrator: Task 3

2. **Placeholder scan:** No TBD/TODO.

3. **Type consistency:** `Agent` still exposes public methods; sub-components are internal.
