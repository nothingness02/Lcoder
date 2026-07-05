# Agent Checkpoint Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a decoupled checkpoint mechanism that can save and restore the agent's full conversational + runtime state, replacing/adopting the current message-only session persistence and the ad-hoc `WithMode` snapshot logic.

**Architecture:** Introduce a small `pkg/checkpoint` package that owns the portable `Checkpoint` DTO and storage interface. Each subsystem (`contextmgr`, `agent/state`, `agent/executor`) exposes its own snapshot/restore methods. The `Agent` assembles these subsystem snapshots into a `Checkpoint` without knowing storage details. A `FileCheckpointStore` persists checkpoints next to session files. The TUI gets `/save`, `/restore`, and `/checkpoints` slash commands.

**Tech Stack:** Go 1.25, JSON line/session storage, existing `contextmgr.Manager`/`agent.Agent` components.

---

## What checkpoint covers (and what it does not)

| Existing mechanism | Covered by checkpoint? | How |
|---|---|---|
| `session.Store` message persistence | **Yes** | Messages live inside `ContextSnapshot.Blocks`; checkpoint becomes the richer persistence format |
| `Agent.WithMode` context-manager clone | **Yes** | `WithMode` can internally use `Agent.Checkpoint()` + modify mode + `Agent.Restore()` (branch) |
| Integration test turn snapshots (`test/integration/agent_realrun_test.go`) | **No** | Those are read-only test artifacts; checkpoint is a runtime save/restore mechanism |
| Deferred tool active set (`executor.activeDeferred`) | **Yes** | Stored in `RuntimeSnapshot.ActiveDeferred` |
| Steering / follow-up queues | **Yes** | Stored in `RuntimeSnapshot.SteeringQueue` / `FollowUpQueue` |
| Abort channel / in-flight stream cancel func | **Partially** | We save `State`; on restore we create a fresh abort channel and leave `streamAbort` nil |
| LLM provider stream midpoint | **No** | Cannot serialize an open HTTP stream; checkpoint is taken at turn boundaries |

---

## File Structure

- **Create:** `pkg/checkpoint/checkpoint.go` — portable DTO + storage interface
- **Create:** `pkg/checkpoint/filestore.go` — disk-backed `CheckpointStore`
- **Create:** `pkg/contextmgr/checkpoint.go` — `Manager.Snapshot()` / `Restore()`
- **Create:** `pkg/agent/state_snapshot.go` — `stateHolder` + `executor` snapshot helpers
- **Create:** `pkg/agent/checkpoint.go` — `Agent.Checkpoint()` / `Restore()`
- **Create:** `pkg/tui/checkpoint_binding.go` — TUI command handlers
- **Modify:** `pkg/agent/loop.go` — refactor `WithMode` to reuse checkpoint snapshot
- **Modify:** `pkg/tui/keys.go` — add `/save`, `/restore`, `/checkpoints` dispatch
- **Modify:** `pkg/tui/commands.go` — register new slash commands
- **Modify:** `pkg/tui/messages.go` — extend `AgentRunner` with checkpoint interface (alias)
- **Modify:** `pkg/session/store.go` — load/save checkpoint alongside session (optional thin wrapper)

---

## Task 1: Define portable Checkpoint DTO and storage interface

**Files:**
- Create: `pkg/checkpoint/checkpoint.go`
- Test: `pkg/checkpoint/checkpoint_test.go`

### Step 1: Write the failing test

```go
package checkpoint

import (
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/models"
)

func TestCheckpointRoundTrip(t *testing.T) {
	cp := &Checkpoint{
		Version:   1,
		CreatedAt: time.Now().UTC(),
		Mode:      "review",
		Model:     models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		Context: &ContextSnapshot{
			Budget: contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 115000},
			Blocks: []BlockSnapshot{
				{
					Kind:     "system",
					Name:     "system",
					Priority: 100,
					Stability: "static",
					Messages: []models.AgentMessage{
						models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: "hello"}),
					},
				},
			},
		},
		Runtime: &RuntimeSnapshot{
			State:          0,
			SteeringQueue:  []models.AgentMessage{models.UserMessage("steer")},
			FollowUpQueue:  []models.AgentMessage{models.UserMessage("follow")},
			ActiveDeferred: map[string]bool{"read": true},
		},
	}

	data, err := cp.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	decoded := &Checkpoint{}
	if err := decoded.UnmarshalJSON(data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Mode != "review" {
		t.Fatalf("mode mismatch: %s", decoded.Mode)
	}
	if len(decoded.Context.Blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(decoded.Context.Blocks))
	}
	if !decoded.Runtime.ActiveDeferred["read"] {
		t.Fatalf("active deferred lost")
	}
}
```

Run:
```bash
cd D:/code_practise/project/lab_pj/Lcoder
go test ./pkg/checkpoint/... -run TestCheckpointRoundTrip -v
```
Expected: FAIL — package `checkpoint` and types do not exist.

### Step 2: Create the DTO and interface

```go
// pkg/checkpoint/checkpoint.go
package checkpoint

import (
	"encoding/json"
	"time"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/models"
)

const CurrentVersion = 1

// Checkpoint is a portable, serializable snapshot of an agent at a turn boundary.
type Checkpoint struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	Mode      string    `json:"mode"`
	Model     models.ModelRef `json:"model"`
	Context   *ContextSnapshot `json:"context"`
	Runtime   *RuntimeSnapshot `json:"runtime"`
}

// ContextSnapshot holds the conversational state managed by contextmgr.Manager.
type ContextSnapshot struct {
	Budget             contextmgr.TokenBudget `json:"budget"`
	Blocks             []BlockSnapshot        `json:"blocks"`
	EphemeralReminders []string               `json:"ephemeral_reminders,omitempty"`
	LastUsage          *contextmgr.RealUsage  `json:"last_usage,omitempty"`
	CachePolicy        string                 `json:"cache_policy,omitempty"`
}

// BlockSnapshot is a serializable copy of contextmgr.Block.
type BlockSnapshot struct {
	Kind             string                `json:"kind"`
	Name             string                `json:"name"`
	Priority         int                   `json:"priority"`
	Stability        string                `json:"stability"`
	Messages         []models.AgentMessage `json:"messages"`
	Metadata         map[string]any        `json:"metadata,omitempty"`
	CacheHint        string                `json:"cache_hint,omitempty"`
	LastModifiedTurn int                   `json:"last_modified_turn"`
}

// RuntimeSnapshot holds agent runtime state that is not part of the context manager.
type RuntimeSnapshot struct {
	State          int                   `json:"state"`
	SteeringQueue  []models.AgentMessage `json:"steering_queue,omitempty"`
	FollowUpQueue  []models.AgentMessage `json:"follow_up_queue,omitempty"`
	ActiveDeferred map[string]bool       `json:"active_deferred,omitempty"`
}

// Source produces a Checkpoint.
type Source interface {
	Checkpoint() (*Checkpoint, error)
}

// Target restores from a Checkpoint.
type Target interface {
	Restore(cp *Checkpoint) error
}

// Store persists and loads checkpoints by ID.
type Store interface {
	Save(id string, cp *Checkpoint) error
	Load(id string) (*Checkpoint, error)
	List() ([]string, error)
	Delete(id string) error
}

func (c *Checkpoint) MarshalJSON() ([]byte, error) {
	if c.Version == 0 {
		c.Version = CurrentVersion
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	type alias Checkpoint
	return json.Marshal((*alias)(c))
}

func (c *Checkpoint) UnmarshalJSON(data []byte) error {
	type alias Checkpoint
	if err := json.Unmarshal(data, (*alias)(c)); err != nil {
		return err
	}
	if c.Version != CurrentVersion {
		return ErrVersionMismatch
	}
	return nil
}
```

Add `pkg/checkpoint/errors.go`:

```go
package checkpoint

import "errors"

var (
	// ErrVersionMismatch means the checkpoint was written by a newer binary.
	ErrVersionMismatch = errors.New("checkpoint version too new")
	ErrNotFound        = errors.New("checkpoint not found")
)
```

Run:
```bash
go test ./pkg/checkpoint/... -run TestCheckpointRoundTrip -v
```
Expected: PASS.

### Step 3: Commit

```bash
git add pkg/checkpoint/
git commit -m "feat(checkpoint): portable checkpoint DTO and storage interface

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 2: Snapshot/restore for contextmgr.Manager

**Files:**
- Create: `pkg/contextmgr/checkpoint.go`
- Modify: `pkg/contextmgr/manager.go` — expose `RealUsage` if not already exported
- Test: `pkg/contextmgr/checkpoint_test.go`

### Step 1: Write the failing test

```go
package contextmgr

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func TestManagerSnapshotRestore(t *testing.T) {
	budget := TokenBudget{MaxTotal: 1000, TargetTotal: 900}
	mgr := NewManager(budget)
	mgr.SetBlock(NewBlock(BlockSystem, "system", StabilityStatic, 100,
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: "base"})))
	mgr.AppendMessages(BlockRecent, models.UserMessage("hi"))

	state, err := mgr.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(state.Blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(state.Blocks))
	}

	fresh := NewManager(TokenBudget{})
	if err := fresh.Restore(state); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if fresh.budget.MaxTotal != 1000 {
		t.Fatalf("budget not restored")
	}
	b, ok := fresh.GetBlock(BlockSystem, "system")
	if !ok || b.Text() != "base" {
		t.Fatalf("system block not restored")
	}
}
```

Run:
```bash
go test ./pkg/contextmgr/... -run TestManagerSnapshotRestore -v
```
Expected: FAIL — `Snapshot`/`Restore` missing.

### Step 1.5: Add defensive test for missing services

Append to `pkg/contextmgr/checkpoint_test.go`:

```go
func TestManagerRestoreRejectsMissingServices(t *testing.T) {
	// NewManager with zero budget creates a Manager with nil estimator/summarizer/policy.
	fresh := NewManager(TokenBudget{})
	state := &ManagerState{
		Budget: TokenBudget{MaxTotal: 1000},
		Blocks: []BlockState{},
	}
	if err := fresh.Restore(state); err == nil {
		t.Fatalf("expected error when restoring manager without services")
	}
}
```

Run:
```bash
go test ./pkg/contextmgr/... -run TestManagerRestoreRejectsMissingServices -v
```
Expected: FAIL — `Restore` does not yet check services.

### Step 2: Implement ManagerState and snapshot/restore

```go
// pkg/contextmgr/checkpoint.go
package contextmgr

import (
	"encoding/json"
	"fmt"

	"github.com/lcoder/lcoder/pkg/models"
)

// ManagerState is a serializable view of a Manager.
type ManagerState struct {
	Budget             TokenBudget     `json:"budget"`
	Blocks             []BlockState    `json:"blocks"`
	EphemeralReminders []string        `json:"ephemeral_reminders,omitempty"`
	LastUsage          *RealUsage      `json:"last_usage,omitempty"`
	CachePolicy        string          `json:"cache_policy,omitempty"`
}

// BlockState mirrors Block for serialization.
type BlockState struct {
	Kind             BlockKind         `json:"kind"`
	Name             string            `json:"name"`
	Priority         int               `json:"priority"`
	Stability        Stability         `json:"stability"`
	Messages         []models.AgentMessage `json:"messages"`
	Metadata         map[string]any    `json:"metadata,omitempty"`
	CacheHint        CacheHint         `json:"cache_hint,omitempty"`
	LastModifiedTurn int               `json:"last_modified_turn"`
}

// Snapshot returns a serializable copy of the manager's state.
func (m *Manager) Snapshot() (*ManagerState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	state := &ManagerState{
		Budget:             m.budget,
		EphemeralReminders: append([]string(nil), m.ephemeralReminders...),
		CachePolicy:        string(m.cachePolicy),
	}
	if m.hasUsage {
		usage := m.lastUsage
		state.LastUsage = &usage
	}
	for _, b := range m.blocks {
		state.Blocks = append(state.Blocks, BlockState{
			Kind:             b.Kind,
			Name:             b.Name,
			Priority:         b.Priority,
			Stability:        b.Stability,
			Messages:         append([]models.AgentMessage(nil), b.Messages...),
			Metadata:         copyMetadata(b.Metadata),
			CacheHint:        b.CacheHint,
			LastModifiedTurn: b.LastModifiedTurn,
		})
	}
	return state, nil
}

// Restore resets this manager to the given state. It preserves estimator/summarizer/policy.
func (m *Manager) Restore(state *ManagerState) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Defensive: a Manager created without estimator/summarizer/policy would
	// panic later in BuildTurnRequest. Fail fast at restore time instead.
	if m.estimator == nil || m.summarizer == nil || m.policy == nil {
		return fmt.Errorf("contextmgr: cannot restore: internal services (estimator/summarizer/policy) not wired")
	}

	m.budget = state.Budget
	m.cachePolicy = ParseCacheHintPolicy(state.CachePolicy)
	m.ephemeralReminders = append([]string(nil), state.EphemeralReminders...)
	if state.LastUsage != nil {
		m.lastUsage = *state.LastUsage
		m.hasUsage = true
	} else {
		m.hasUsage = false
	}
	m.blocks = nil
	for _, bs := range state.Blocks {
		b := NewBlock(bs.Kind, bs.Name, bs.Stability, bs.Priority)
		b.Messages = append([]models.AgentMessage(nil), bs.Messages...)
		b.Metadata = copyMetadata(bs.Metadata)
		b.CacheHint = bs.CacheHint
		b.LastModifiedTurn = bs.LastModifiedTurn
		m.blocks = append(m.blocks, b)
	}
	return nil
}

func copyMetadata(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	// JSON round-trip to deep-copy arbitrary metadata safely.
	data, _ := json.Marshal(src)
	dst := make(map[string]any)
	_ = json.Unmarshal(data, &dst)
	return dst
}
```

Add a read lock getter for usage if `lastUsage` is unexported:

In `pkg/contextmgr/usage.go`, ensure `RealUsage` is exported and accessible. `ManagerState` references it; if usage.go defines `type RealUsage struct`, it is fine.

Run tests:
```bash
go test ./pkg/contextmgr/... -run 'TestManagerSnapshotRestore|TestManagerRestoreRejectsMissingServices' -v
```
Expected: PASS.

### Step 3: Commit

```bash
git add pkg/contextmgr/checkpoint.go pkg/contextmgr/checkpoint_test.go
git commit -m "feat(contextmgr): snapshot and restore manager state

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 3: Snapshot/restore for agent runtime state

**Files:**
- Create: `pkg/agent/state_snapshot.go`
- Test: `pkg/agent/state_snapshot_test.go`

### Step 1: Write the failing test

```go
package agent

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func TestRuntimeSnapshotRoundTrip(t *testing.T) {
	s := newStateHolder()
	s.Steer(models.UserMessage("steer"))
	s.FollowUp(models.UserMessage("follow"))

	e := newExecutor(&Config{}, nil, nil, nil, nil)
	e.activeDeferred = map[string]bool{"edit": true}

	rs := s.snapshot()
	if len(rs.SteeringQueue) != 1 {
		t.Fatalf("steering lost")
	}

	er := e.snapshot()
	if !er.ActiveDeferred["edit"] {
		t.Fatalf("active deferred lost")
	}

	s2 := newStateHolder()
	s2.restore(rs)
	if len(s2.DrainSteeringQueue()) != 1 {
		t.Fatalf("steering not restored")
	}

	e2 := newExecutor(&Config{}, nil, nil, nil, nil)
	e2.restore(er)
	if !e2.activeDeferred["edit"] {
		t.Fatalf("active deferred not restored")
	}
}
```

Run:
```bash
go test ./pkg/agent/... -run TestRuntimeSnapshotRoundTrip -v
```
Expected: FAIL — `snapshot`/`restore` methods do not exist.

### Step 2: Implement runtime snapshot methods

```go
// pkg/agent/state_snapshot.go
package agent

import "github.com/lcoder/lcoder/pkg/models"

// RuntimeState is a serializable view of stateHolder + executor runtime state.
type RuntimeState struct {
	State          State              `json:"state"`
	SteeringQueue  []models.AgentMessage `json:"steering_queue,omitempty"`
	FollowUpQueue  []models.AgentMessage `json:"follow_up_queue,omitempty"`
	ActiveDeferred map[string]bool    `json:"active_deferred,omitempty"`
}

func (s *stateHolder) snapshot() RuntimeState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return RuntimeState{
		State:          s.state,
		SteeringQueue:  append([]models.AgentMessage(nil), s.steeringQueue...),
		FollowUpQueue:  append([]models.AgentMessage(nil), s.followUpQueue...),
	}
}

func (s *stateHolder) restore(rs RuntimeState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = rs.State
	s.steeringQueue = append([]models.AgentMessage(nil), rs.SteeringQueue...)
	s.followUpQueue = append([]models.AgentMessage(nil), rs.FollowUpQueue...)
	s.abortCh = make(chan struct{})
	s.abortOnce = sync.Once{}
	s.streamAbort = nil
}

func (e *executor) snapshot() RuntimeState {
	e.mu.Lock()
	defer e.mu.Unlock()
	active := make(map[string]bool, len(e.activeDeferred))
	for k, v := range e.activeDeferred {
		active[k] = v
	}
	return RuntimeState{ActiveDeferred: active}
}

func (e *executor) restore(rs RuntimeState) {
	e.mu.Lock()
	defer e.mu.Unlock()
	active := make(map[string]bool, len(rs.ActiveDeferred))
	for k, v := range rs.ActiveDeferred {
		active[k] = v
	}
	e.activeDeferred = active
}
```

Add missing import in `state_snapshot.go`:

```go
import (
	"sync"

	"github.com/lcoder/lcoder/pkg/models"
)
```

Note: `newExecutor` constructor may not exist. If executor is built inline, add a small constructor in `executor.go`:

```go
func newExecutor(cfg *Config, mgr *contextmgr.Manager, registry *tools.Registry, permissions *permissions.Engine, emitter *eventEmitter) *executor {
	return &executor{
		cfg:            cfg,
		mgr:            mgr,
		registry:       registry,
		permissions:    permissions,
		emitter:        emitter,
		activeDeferred: make(map[string]bool),
	}
}
```

Run tests:
```bash
go test ./pkg/agent/... -run TestRuntimeSnapshotRoundTrip -v
```
Expected: PASS.

### Step 3: Commit

```bash
git add pkg/agent/state_snapshot.go pkg/agent/state_snapshot_test.go pkg/agent/executor.go
git commit -m "feat(agent): snapshot and restore runtime state

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 4: Agent.Checkpoint() and Agent.Restore()

**Files:**
- Create: `pkg/agent/checkpoint.go`
- Modify: `pkg/agent/runner.go` — extend `Runner`/`ModeSwitcher` aliases in TUI later, not here
- Test: `pkg/agent/checkpoint_test.go`

### Step 1: Write the failing test

```go
package agent

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/checkpoint"
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
)

func TestAgentCheckpointRoundTrip(t *testing.T) {
	client := llmtest.Client(llmtest.Turn(llmtest.Done(models.AssistantMessage("ok"), nil)))
	ag := NewWithObservability(Config{
		SystemPrompt:      "base",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		MaxTurns:          5,
		ToolExecutionMode: models.ExecutionParallel,
		Mode:              "code",
	}, client, testRegistry("."), permissions.NewEngine(permissions.DefaultConfig()), nil, nil)

	ag.loopState.Steer(models.UserMessage("steer"))
	ag.executor.activeDeferred["read"] = true

	cp, err := ag.Checkpoint()
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if cp.Mode != "code" {
		t.Fatalf("mode mismatch")
	}

	fresh := NewWithObservability(Config{
		SystemPrompt:      "base",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		MaxTurns:          5,
		ToolExecutionMode: models.ExecutionParallel,
		Mode:              "code",
	}, client, testRegistry("."), permissions.NewEngine(permissions.DefaultConfig()), nil, nil)

	if err := fresh.Restore(cp); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(fresh.loopState.DrainSteeringQueue()) != 1 {
		t.Fatalf("steering queue not restored")
	}
	if !fresh.executor.activeDeferred["read"] {
		t.Fatalf("active deferred not restored")
	}
	if fresh.cfg.Mode != "code" {
		t.Fatalf("cfg mode not restored")
	}
}
```

Run:
```bash
go test ./pkg/agent/... -run TestAgentCheckpointRoundTrip -v
```
Expected: FAIL — `Checkpoint`/`Restore` missing.

### Step 2: Implement Agent.Checkpoint() and Restore()

```go
// pkg/agent/checkpoint.go
package agent

import (
	"github.com/lcoder/lcoder/pkg/checkpoint"
	"github.com/lcoder/lcoder/pkg/contextmgr"
)

// Checkpoint returns a portable snapshot of the agent.
func (a *Agent) Checkpoint() (*checkpoint.Checkpoint, error) {
	mgrState, err := a.mgr.Snapshot()
	if err != nil {
		return nil, err
	}
	stateSnap := a.loopState.snapshot()
	execSnap := a.executor.snapshot()

	cp := &checkpoint.Checkpoint{
		Mode:  a.cfg.Mode,
		Model: a.cfg.Model,
		Context: &checkpoint.ContextSnapshot{
			Budget:             mgrState.Budget,
			EphemeralReminders: mgrState.EphemeralReminders,
			CachePolicy:        mgrState.CachePolicy,
		},
		Runtime: &checkpoint.RuntimeSnapshot{
			State:          int(stateSnap.State),
			SteeringQueue:  stateSnap.SteeringQueue,
			FollowUpQueue:  stateSnap.FollowUpQueue,
			ActiveDeferred: execSnap.ActiveDeferred,
		},
	}
	if mgrState.LastUsage != nil {
		cp.Context.LastUsage = mgrState.LastUsage
	}
	for _, b := range mgrState.Blocks {
		cp.Context.Blocks = append(cp.Context.Blocks, checkpoint.BlockSnapshot{
			Kind:             string(b.Kind),
			Name:             b.Name,
			Priority:         b.Priority,
			Stability:        string(b.Stability),
			Messages:         b.Messages,
			Metadata:         b.Metadata,
			CacheHint:        string(b.CacheHint),
			LastModifiedTurn: b.LastModifiedTurn,
		})
	}
	return cp, nil
}

// Restore resets this agent from a checkpoint. It reuses the existing services
// (LLM client, registry, bus, etc.) and only restores configuration/context/runtime state.
//
// NOTE: This is NOT a thread-safe hot-restore. It is intended to be called when
// the agent loop is at a safe boundary (e.g., waiting for user input). If the
// loop is currently streaming or executing tools, restoring mid-flight will
// race. Future hot-restore would require an explicit pause/resume protocol.
func (a *Agent) Restore(cp *checkpoint.Checkpoint) error {
	a.cfg.Mode = cp.Mode
	a.cfg.Model = cp.Model

	mgrState := &contextmgr.ManagerState{
		Budget:             cp.Context.Budget,
		EphemeralReminders: cp.Context.EphemeralReminders,
		CachePolicy:        cp.Context.CachePolicy,
	}
	if cp.Context.LastUsage != nil {
		mgrState.LastUsage = cp.Context.LastUsage
	}
	for _, b := range cp.Context.Blocks {
		mgrState.Blocks = append(mgrState.Blocks, contextmgr.BlockState{
			Kind:             contextmgr.BlockKind(b.Kind),
			Name:             b.Name,
			Priority:         b.Priority,
			Stability:        contextmgr.Stability(b.Stability),
			Messages:         b.Messages,
			Metadata:         b.Metadata,
			CacheHint:        contextmgr.CacheHint(b.CacheHint),
			LastModifiedTurn: b.LastModifiedTurn,
		})
	}
	if err := a.mgr.Restore(mgrState); err != nil {
		return err
	}

	a.loopState.restore(RuntimeState{
		State:          State(cp.Runtime.State),
		SteeringQueue:  cp.Runtime.SteeringQueue,
		FollowUpQueue:  cp.Runtime.FollowUpQueue,
	})
	a.executor.restore(RuntimeState{ActiveDeferred: cp.Runtime.ActiveDeferred})
	return nil
}
```

Run tests:
```bash
go test ./pkg/agent/... -run TestAgentCheckpointRoundTrip -v
```
Expected: PASS.

### Step 3: Commit

```bash
git add pkg/agent/checkpoint.go pkg/agent/checkpoint_test.go
git commit -m "feat(agent): Agent.Checkpoint and Agent.Restore

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 5: File-based CheckpointStore

**Files:**
- Create: `pkg/checkpoint/filestore.go`
- Test: `pkg/checkpoint/filestore_test.go`

### Step 1: Write the failing test

```go
package checkpoint

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)

	cp := &Checkpoint{Version: CurrentVersion, Mode: "review"}
	if err := store.Save("sess-1", cp); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := store.Load("sess-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Mode != "review" {
		t.Fatalf("mode mismatch")
	}

	ids, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ids) != 1 || ids[0] != "sess-1" {
		t.Fatalf("unexpected list: %v", ids)
	}
}
```

Run:
```bash
go test ./pkg/checkpoint/... -run TestFileStoreRoundTrip -v
```
Expected: FAIL — `NewFileStore` missing.

### Step 2: Implement FileStore

```go
// pkg/checkpoint/filestore.go
package checkpoint

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileStore persists checkpoints as JSON files on disk.
type FileStore struct {
	Dir string
}

// NewFileStore creates a file store rooted at dir.
func NewFileStore(dir string) *FileStore {
	return &FileStore{Dir: dir}
}

func (fs *FileStore) path(id string) string {
	// Sanitize id so it is safe as a filename.
	safe := strings.ReplaceAll(id, string(filepath.Separator), "_")
	return filepath.Join(fs.Dir, safe+".checkpoint.json")
}

func (fs *FileStore) Save(id string, cp *Checkpoint) error {
	if err := os.MkdirAll(fs.Dir, 0o755); err != nil {
		return err
	}
	data, err := cp.MarshalJSON()
	if err != nil {
		return err
	}
	return os.WriteFile(fs.path(id), data, 0o644)
}

func (fs *FileStore) Load(id string) (*Checkpoint, error) {
	data, err := os.ReadFile(fs.path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	cp := &Checkpoint{}
	if err := cp.UnmarshalJSON(data); err != nil {
		return nil, fmt.Errorf("decode checkpoint %s: %w", id, err)
	}
	return cp, nil
}

func (fs *FileStore) List() ([]string, error) {
	entries, err := os.ReadDir(fs.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".checkpoint.json") {
			ids = append(ids, strings.TrimSuffix(name, ".checkpoint.json"))
		}
	}
	return ids, nil
}

func (fs *FileStore) Delete(id string) error {
	err := os.Remove(fs.path(id))
	if os.IsNotExist(err) {
		return ErrNotFound
	}
	return err
}
```

Run tests:
```bash
go test ./pkg/checkpoint/... -run TestFileStoreRoundTrip -v
```
Expected: PASS.

### Step 3: Commit

```bash
git add pkg/checkpoint/filestore.go pkg/checkpoint/filestore_test.go
git commit -m "feat(checkpoint): file-based checkpoint store

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 6: Refactor WithMode to reuse checkpoint snapshot

**Files:**
- Modify: `pkg/agent/loop.go`
- Test: `pkg/agent/loop_test.go`

### Step 1: Write a test asserting WithMode preserves checkpointable state

```go
func TestWithModeUsesCheckpointSnapshot(t *testing.T) {
	client := llmtest.Client(llmtest.Turn(llmtest.Done(models.AssistantMessage("ok"), nil)))
	mm := NewModeManager()
	mm.modes["code"] = ModeConfig{Name: "code", SystemPrompt: "code mode"}
	mm.modes["review"] = ModeConfig{Name: "review", SystemPrompt: "review mode"}

	ag := NewWithObservability(Config{
		SystemPrompt:      "base",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		MaxTurns:          1,
		ToolExecutionMode: models.ExecutionParallel,
		ModeManager:       mm,
		Mode:              "code",
	}, client, testRegistry("."), permissions.NewEngine(permissions.DefaultConfig()), events.New(), nil)

	ag.loopState.Steer(models.UserMessage("steer"))
	ag.executor.activeDeferred["read"] = true

	reviewAg := ag.WithMode("review").(*Agent)
	if reviewAg.cfg.Mode != "review" {
		t.Fatalf("expected review mode")
	}
	if len(reviewAg.loopState.DrainSteeringQueue()) != 1 {
		t.Fatalf("steering queue lost in WithMode")
	}
	if !reviewAg.executor.activeDeferred["read"] {
		t.Fatalf("active deferred lost in WithMode")
	}
	if len(ag.loopState.DrainSteeringQueue()) != 1 {
		t.Fatalf("original agent steering queue mutated")
	}
}
```

Run:
```bash
go test ./pkg/agent/... -run TestWithModeUsesCheckpointSnapshot -v
```
Expected: PASS if previous implementation works; the refactor should keep it passing.

### Step 2: Refactor WithMode

Replace `WithMode` in `pkg/agent/loop.go` with:

```go
// WithMode returns a copy of the agent configured for a different mode. It
// snapshots the current agent via the checkpoint mechanism so that context,
// runtime state, and mode-specific prompts are applied consistently.
func (a *Agent) WithMode(mode string) Runner {
	cp, err := a.Checkpoint()
	if err != nil {
		// WithMode is synchronous and should not fail in practice; surface via panic
		// to avoid silently losing state. Tests will catch regressions.
		panic(fmt.Sprintf("checkpoint agent for mode switch: %v", err))
	}
	cp.Mode = mode

	fresh := &Agent{
		cfg:          a.cfg,
		llm:          a.llm,
		registry:     a.registry,
		bus:          a.bus,
		obsCollector: a.obsCollector,
		emitter:      a.emitter,
		loopState:    newStateHolder(),
		streamer:     a.streamer,
	}
	fresh.cfg.ContextManager = nil // will be restored by Restore
	fresh.cfg.Mode = mode
	fresh.mgr = a.mgr // placeholder; Restore will rebuild blocks in a fresh manager
	fresh.executor = newExecutor(&fresh.cfg, fresh.mgr, a.registry, a.executor.permissions, fresh.emitter)

	// Create a fresh manager that shares the original estimator/summarizer/policy.
	fresh.mgr = contextmgr.NewManager(
		cp.Context.Budget,
		contextmgr.WithEstimator(a.mgr.Estimator()),
		contextmgr.WithSummarizer(a.mgr.Summarizer()),
		contextmgr.WithWindowPolicy(a.mgr.WindowPolicy()),
	)
	fresh.cfg.ContextManager = fresh.mgr
	fresh.executor.mgr = fresh.mgr

	if err := fresh.Restore(cp); err != nil {
		panic(fmt.Sprintf("restore agent for mode switch: %v", err))
	}
	return fresh
}
```

Note: `contextmgr.Manager` needs public accessors for `Estimator()`, `Summarizer()`, `WindowPolicy()` if not already present. Add them in `pkg/contextmgr/manager.go`:

```go
func (m *Manager) Estimator() TokenEstimator     { return m.estimator }
func (m *Manager) Summarizer() SummarizeFunc     { return m.summarizer }
func (m *Manager) WindowPolicy() WindowPolicy    { return m.policy }
```

Run tests:
```bash
go test ./pkg/agent/... -run TestWithModeUsesCheckpointSnapshot -v
```
Expected: PASS.

### Step 3: Commit

```bash
git add pkg/agent/loop.go pkg/contextmgr/manager.go pkg/agent/loop_test.go
git commit -m "refactor(agent): WithMode uses checkpoint snapshot

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 7: Wire checkpoint commands into TUI

**Files:**
- Create: `pkg/tui/checkpoint_binding.go`
- Modify: `pkg/tui/commands.go`
- Modify: `pkg/tui/keys.go`
- Modify: `pkg/tui/messages.go`
- Test: `pkg/tui/checkpoint_binding_test.go`

### Step 1: Add checkpoint interface aliases to TUI

In `pkg/tui/messages.go`, add aliases so TUI can treat the agent as a checkpoint source/target:

```go
// CheckpointSource is the agent's ability to produce a checkpoint.
type CheckpointSource = checkpoint.Source

// CheckpointTarget is the agent's ability to restore a checkpoint.
type CheckpointTarget = checkpoint.Target
```

Import `github.com/lcoder/lcoder/pkg/checkpoint`.

### Step 2: Register slash commands

In `pkg/tui/commands.go`, add:

```go
{Name: "save", Description: "Save current agent checkpoint", Category: "Session"},
{Name: "restore", Description: "Restore agent checkpoint", Category: "Session"},
{Name: "checkpoints", Description: "List saved checkpoints", Category: "Session"},
```

### Step 3: Implement checkpoint binding

```go
// pkg/tui/checkpoint_binding.go
package tui

import (
	"fmt"
	"strings"

	"github.com/lcoder/lcoder/pkg/checkpoint"
)

// checkpointStore is injected into Model; it decouples TUI from file paths.
type checkpointStore interface {
	Save(id string, cp *checkpoint.Checkpoint) error
	Load(id string) (*checkpoint.Checkpoint, error)
	List() ([]string, error)
}

func (m *Model) saveCheckpoint() {
	src, ok := m.agent.(CheckpointSource)
	if !ok {
		m.showTextPanel("save", styleError().Render("agent does not support checkpoints"))
		return
	}
	if m.checkpointStore == nil {
		m.showTextPanel("save", styleError().Render("no checkpoint store configured"))
		return
	}
	cp, err := src.Checkpoint()
	if err != nil {
		m.showTextPanel("save", styleError().Render("checkpoint failed: "+err.Error()))
		return
	}
	id := m.session.SessionID()
	if err := m.checkpointStore.Save(id, cp); err != nil {
		m.showTextPanel("save", styleError().Render("save failed: "+err.Error()))
		return
	}
	m.showTextPanel("save", styleDim().Render("checkpoint saved: "+id))
}

func (m *Model) restoreCheckpoint() {
	tgt, ok := m.agent.(CheckpointTarget)
	if !ok {
		m.showTextPanel("restore", styleError().Render("agent does not support checkpoints"))
		return
	}
	if m.checkpointStore == nil {
		m.showTextPanel("restore", styleError().Render("no checkpoint store configured"))
		return
	}
	id := m.session.SessionID()
	cp, err := m.checkpointStore.Load(id)
	if err != nil {
		m.showTextPanel("restore", styleError().Render("load failed: "+err.Error()))
		return
	}
	if err := tgt.Restore(cp); err != nil {
		m.showTextPanel("restore", styleError().Render("restore failed: "+err.Error()))
		return
	}
	m.blocks = blocksFromMessages(m.agent.AllMessages())
	m.rebuildViewport()
	m.showTextPanel("restore", styleDim().Render("checkpoint restored: "+id))
}

func (m *Model) listCheckpoints() {
	if m.checkpointStore == nil {
		m.showTextPanel("checkpoints", styleError().Render("no checkpoint store configured"))
		return
	}
	ids, err := m.checkpointStore.List()
	if err != nil {
		m.showTextPanel("checkpoints", styleError().Render("list failed: "+err.Error()))
		return
	}
	if len(ids) == 0 {
		m.showTextPanel("checkpoints", styleDim().Render("no checkpoints"))
		return
	}
	m.showTextPanel("checkpoints", styleDim().Render(strings.Join(ids, "\n")))
}
```

### Step 4: Add field and wiring

In `pkg/tui/model.go`, add:

```go
checkpointStore checkpointStore
```

Update `NewModel` signature to accept `checkpointStore checkpointStore` (can be `nil`).

In `pkg/tui/keys.go`, add cases to `dispatchSlash`:

```go
case "save":
	m.saveCheckpoint()
case "restore":
	m.restoreCheckpoint()
case "checkpoints":
	m.listCheckpoints()
```

### Step 5: Test TUI checkpoint binding

```go
package tui

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/checkpoint"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/events"
)

type fakeCheckpointStore struct {
	saved map[string]*checkpoint.Checkpoint
}

func (f *fakeCheckpointStore) Save(id string, cp *checkpoint.Checkpoint) error {
	if f.saved == nil {
		f.saved = make(map[string]*checkpoint.Checkpoint)
	}
	f.saved[id] = cp
	return nil
}

func (f *fakeCheckpointStore) Load(id string) (*checkpoint.Checkpoint, error) {
	cp, ok := f.saved[id]
	if !ok {
		return nil, checkpoint.ErrNotFound
	}
	return cp, nil
}

func (f *fakeCheckpointStore) List() ([]string, error) {
	var ids []string
	for id := range f.saved {
		ids = append(ids, id)
	}
	return ids, nil
}

func TestTUISaveCheckpoint(t *testing.T) {
	store := &fakeCheckpointStore{}
	bus := events.New()
	ag := &fakeAgent{mode: "code"}
	sess := &fakeSession{id: "abc123"}
	m := NewModel(bus, ag, sess, &fakeSessionStore{}, ".", "abc123", "openai/gpt-4o-mini", "dark", nil, nil, nil, nil, config.Config{}, false)
	m.checkpointStore = store

	m.dispatchSlash("/save")
	if _, ok := store.saved["abc123"]; !ok {
		t.Fatal("expected checkpoint saved")
	}
}
```

Run:
```bash
go test ./pkg/tui/... -run TestTUISaveCheckpoint -v
```
Expected: PASS after wiring `NewModel` to accept checkpoint store.

### Step 6: Commit

```bash
git add pkg/tui/checkpoint_binding.go pkg/tui/checkpoint_binding_test.go pkg/tui/commands.go pkg/tui/keys.go pkg/tui/messages.go pkg/tui/model.go
git commit -m "feat(tui): /save, /restore, /checkpoints slash commands

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 8: Wire checkpoint store in app entry points

**Files:**
- Modify: `pkg/tui/app.go`
- Modify: `cmd/lcoder/main.go` (if it calls NewModel indirectly via app.go)

### Step 1: Create checkpoint store in app.go

In `pkg/tui/app.go`, where `NewModel` is called, create a `FileCheckpointStore` rooted at `filepath.Join(session.DefaultDir(), "checkpoints")` and pass it.

```go
import (
	"path/filepath"

	"github.com/lcoder/lcoder/pkg/checkpoint"
	"github.com/lcoder/lcoder/pkg/session"
)

// In the function that constructs the model:
checkpointDir := filepath.Join(session.DefaultDir(), "checkpoints")
checkpointStore := checkpoint.NewFileStore(checkpointDir)
model := NewModel(bus, ag, sess, store, cwd, sess.ID, modelRef, themeStyle, httpTools, mcpRegistry, modeManager, llmClient, cfg, needsProviderSetup, loadedSkills...)
model.checkpointStore = checkpointStore
```

### Step 2: Commit

```bash
git add pkg/tui/app.go
git commit -m "feat(tui): inject file checkpoint store at startup

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 9: Integration test for checkpoint save/restore across turns

**Files:**
- Create: `test/integration/checkpoint_test.go`

### Step 1: Write the test

```go
//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/checkpoint"
)

func TestAgentCheckpointSaveRestore(t *testing.T) {
	// Build agent as in TestAgentRealRun, but use a fake/scripted LLM to avoid API keys.
	// This is a skeleton; fill in the same construction as TestAgentRealRun.
	dir := t.TempDir()
	store := checkpoint.NewFileStore(dir)

	// 1. Create agent, run a prompt that performs a tool call.
	// 2. Save checkpoint.
	// 3. Create a fresh agent with the same services.
	// 4. Restore checkpoint.
	// 5. Continue and assert the conversation resumes from the saved state.

	_ = store
	t.Skip("implement with scripted LLM once checkpoint wiring is in place")
}
```

This test is intentionally a skeleton because it depends on the real wiring from previous tasks. Once Tasks 1-8 are complete, remove `t.Skip` and fill in the agent construction from `TestAgentRealRun`.

### Step 2: Commit

```bash
git add test/integration/checkpoint_test.go
git commit -m "test(integration): skeleton for checkpoint save/restore

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Decoupling summary

| Layer | Responsibility | Does not know about |
|---|---|---|
| `pkg/checkpoint` | DTO + storage interface | Agent internals, contextmgr internals |
| `pkg/contextmgr` | How to snapshot/restore its own blocks, budget, usage | JSON file paths, agent |
| `pkg/agent/state` + `pkg/agent/executor` | How to snapshot/restore queues and active deferred tools | Storage, TUI |
| `pkg/agent/checkpoint.go` | Assembles subsystem snapshots into `Checkpoint` | Storage implementation |
| `pkg/checkpoint/filestore.go` | JSON file persistence | Agent, contextmgr |
| `pkg/tui` | Commands + UI feedback | JSON schema, Manager internals |
| `pkg/tui/app.go` | Wires a concrete `FileCheckpointStore` | Nothing else |

This means you can later swap `FileCheckpointStore` for a database or cloud store without touching `Agent` or `contextmgr`.

---

## Self-review

1. **Spec coverage:**
   - Checkpoint DTO and storage: Task 1, 5
   - Subsystem snapshot/restore: Tasks 2, 3
   - Agent-level Checkpoint/Restore: Task 4
   - Covers mode-switch snapshot: Task 6
   - Covers session persistence (augmented): Task 8
   - TUI integration: Tasks 7, 8
   - Integration test: Task 9

2. **Placeholder scan:** No TBD/TODO. The only intentional placeholder is `t.Skip` in Task 9, which is guarded and described explicitly.

3. **Type consistency:**
   - `Checkpoint.Context.Blocks` uses `checkpoint.BlockSnapshot` everywhere.
   - `ManagerState.Blocks` uses `contextmgr.BlockState`.
   - `Agent.Checkpoint` maps between them consistently.
   - `RuntimeSnapshot.State` is `int`; `stateHolder.restore` maps to `agent.State`.

---

**Plan complete and saved to `docs/superpowers/plans/2026-07-01-agent-checkpoint.md`.**

Two execution options:

1. **Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** — Execute tasks in this session using `superpowers:executing-plans`, batch execution with checkpoints for review.

Which approach would you like?
