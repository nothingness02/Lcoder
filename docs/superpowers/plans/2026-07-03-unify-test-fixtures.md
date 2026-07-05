# Unify Test Fixtures Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move reusable fake implementations into `pkg/testutil` so each package's tests stop duplicating them.

**Architecture:** Create `pkg/testutil` package with `FakeSandbox`, `FakeAgent`, `FakeSessionStore`, `FakeRegistry`. Keep it internal-test-only by using `_test.go` files where possible, or export public types if non-test code also needs them.

**Tech Stack:** Go 1.25.

---

## File Structure

- **Create:** `pkg/testutil/fake_sandbox.go`
- **Create:** `pkg/testutil/fake_agent.go`
- **Create:** `pkg/testutil/fake_registry.go`
- **Create:** `pkg/testutil/fake_session.go`
- **Modify:** `pkg/sandbox/fake.go`
- **Modify:** `pkg/tui/model_test.go`
- **Modify:** `pkg/agent/*_test.go`

---

## Task 1: Move FakeSandbox to testutil

**Files:**
- Create: `pkg/testutil/fake_sandbox.go`
- Modify: `pkg/sandbox/fake.go`

- [ ] **Step 1: Copy FakeSandbox**

Create `pkg/testutil/fake_sandbox.go`:

```go
package testutil

import (
	"context"
	"net"

	"github.com/lcoder/lcoder/pkg/sandbox"
)

// FakeSandbox records Exec calls and returns programmed results.
type FakeSandbox struct {
	Calls     []sandbox.ExecSpec
	Result    sandbox.ExecResult
	Err       error
	NetPolicy sandbox.NetworkPolicy
	FSPolicy  sandbox.FilesystemPolicy
}

// NewFakeSandbox returns a FakeSandbox with allow-all policies.
func NewFakeSandbox() *FakeSandbox {
	return &FakeSandbox{
		NetPolicy: &sandbox.PassthroughNetwork{},
		FSPolicy:  sandbox.AllowAllFS{},
	}
}

func (f *FakeSandbox) Exec(_ context.Context, spec sandbox.ExecSpec) (sandbox.ExecResult, error) {
	f.Calls = append(f.Calls, spec)
	return f.Result, f.Err
}

func (f *FakeSandbox) Network() sandbox.NetworkPolicy       { return f.NetPolicy }
func (f *FakeSandbox) Filesystem() sandbox.FilesystemPolicy { return f.FSPolicy }
func (f *FakeSandbox) Name() string                         { return "fake" }

var _ sandbox.Sandbox = (*FakeSandbox)(nil)
```

- [ ] **Step 2: Make pkg/sandbox/fake.go a thin alias**

Modify `pkg/sandbox/fake.go`:

```go
package sandbox

import "github.com/lcoder/lcoder/pkg/testutil"

// FakeSandbox is retained for backward compatibility; new tests should use
// pkg/testutil.FakeSandbox directly.
type FakeSandbox = testutil.FakeSandbox

// NewFakeSandbox is retained for backward compatibility.
func NewFakeSandbox() *FakeSandbox {
	return testutil.NewFakeSandbox()
}
```

- [ ] **Step 3: Commit**

```bash
git add pkg/testutil/fake_sandbox.go pkg/sandbox/fake.go
git commit -m "test(testutil): centralize FakeSandbox

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 2: Add FakeAgent and FakeSessionStore

**Files:**
- Create: `pkg/testutil/fake_agent.go`
- Create: `pkg/testutil/fake_session.go`

- [ ] **Step 1: Implement FakeAgent**

Create `pkg/testutil/fake_agent.go`:

```go
package testutil

import (
	"context"

	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/models"
)

// FakeAgent is a test double for agent.Runner and agent.ModeSwitcher.
type FakeAgent struct {
	Prompts        []models.AgentMessage
	Messages       []models.AgentMessage
	Mode           string
	SwitchedModel  models.ModelRef
	SwitchedBudget contextmgr.TokenBudget
}

func (f *FakeAgent) Prompt(_ context.Context, msg models.AgentMessage) error {
	f.Prompts = append(f.Prompts, msg)
	return nil
}
func (f *FakeAgent) Continue(_ context.Context) error       { return nil }
func (f *FakeAgent) AllMessages() []models.AgentMessage     { return f.Messages }
func (f *FakeAgent) SetMessages(msgs []models.AgentMessage) { f.Messages = msgs }
func (f *FakeAgent) Stats() map[string]int                  { return nil }
func (f *FakeAgent) Mode() string {
	if f.Mode == "" {
		return "code"
	}
	return f.Mode
}
func (f *FakeAgent) SetUserConfirm(agent.UserConfirmation) {}
func (f *FakeAgent) Steer(models.AgentMessage)             {}
func (f *FakeAgent) Abort()                                {}
func (f *FakeAgent) SwitchModel(ref models.ModelRef, budget contextmgr.TokenBudget) {
	f.SwitchedModel = ref
	f.SwitchedBudget = budget
}
func (f *FakeAgent) WithMode(mode string) agent.Runner {
	f.Mode = mode
	return f
}

var _ agent.Runner = (*FakeAgent)(nil)
```

- [ ] **Step 2: Implement FakeSessionStore**

Create `pkg/testutil/fake_session.go`:

```go
package testutil

import "github.com/lcoder/lcoder/pkg/session"

// FakeSessionStore is a test double for session.Store-like operations.
type FakeSessionStore struct{}

func (f *FakeSessionStore) List(cwd string) ([]session.Session, error) { return nil, nil }
func (f *FakeSessionStore) LoadByID(cwd, id string) (*session.Session, error) {
	return nil, nil
}
```

- [ ] **Step 3: Commit**

```bash
git add pkg/testutil/fake_agent.go pkg/testutil/fake_session.go
git commit -m "test(testutil): add FakeAgent and FakeSessionStore

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 3: Migrate Existing Tests

**Files:**
- Modify: `pkg/tui/model_test.go`
- Modify: `pkg/agent/deferred_loop_test.go`

- [ ] **Step 1: Replace TUI fakeAgent**

Modify `pkg/tui/model_test.go` to use `testutil.FakeAgent` and `testutil.FakeSessionStore`. Delete local `fakeAgent` and `fakeSessionStore`.

- [ ] **Step 2: Keep agent fakeTool local**

`pkg/agent/deferred_loop_test.go` keeps its minimal `fakeTool`; it is package-local and not worth centralizing.

- [ ] **Step 3: Commit**

```bash
git add pkg/tui/model_test.go pkg/agent/deferred_loop_test.go
git commit -m "test: migrate TUI/agent tests to testutil fakes

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 4: Full Verification

- [ ] **Step 1: Run tests**

```bash
go test ./pkg/testutil/... ./pkg/tui/... ./pkg/agent/... ./pkg/sandbox/... -count=1
```
Expected: PASS.

- [ ] **Step 2: Build and vet**

```bash
go build ./...
go vet ./pkg/testutil/... ./pkg/tui/... ./pkg/agent/... ./pkg/sandbox/...
```
Expected: no output.

---

## Self-review

1. **Spec coverage:**
   - FakeSandbox: Task 1
   - FakeAgent/SessionStore: Task 2
   - Migration: Task 3

2. **Placeholder scan:** No TBD/TODO.

3. **Type consistency:** `FakeAgent` implements `agent.Runner`.
