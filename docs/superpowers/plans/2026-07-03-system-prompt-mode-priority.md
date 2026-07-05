# System Prompt and Mode Priority Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the precedence relationship between the base system prompt and mode-specific system prompt explicit, configurable, and documented.

**Architecture:** Add a `ModePromptPriority` enum to `config.ContextConfig`. In `pkg/agentsetup` and `pkg/agent`, use the enum to decide whether the mode prompt is appended after the base system prompt (current behavior), prepended before it, or replaces it entirely. Default remains `append`. Add unit tests and user-facing docs.

**Tech Stack:** Go 1.25, `pkg/config`, `pkg/agentsetup`, `pkg/agent`.

---

## File Structure

- **Modify:** `pkg/config/config.go`
- **Modify:** `pkg/agentsetup/setup.go`
- **Modify:** `pkg/agent/loop.go`
- **Create:** `pkg/agentsetup/setup_mode_priority_test.go`
- **Modify:** `configs/lcoder.yaml`
- **Create:** `docs/modes-and-system-prompt.md`

---

## Task 1: Add Mode Prompt Priority Config

**Files:**
- Modify: `pkg/config/config.go`
- Modify: `configs/lcoder.yaml`

- [ ] **Step 1: Add config type and field**

In `pkg/config/config.go`, add:

```go
// ModePromptPriority controls how mode-specific system prompt relates to the
// base system prompt.
type ModePromptPriority string

const (
	// ModePromptAppend places the mode prompt after the base system prompt.
	ModePromptAppend ModePromptPriority = "append"
	// ModePromptPrepend places the mode prompt before the base system prompt.
	ModePromptPrepend ModePromptPriority = "prepend"
	// ModePromptReplace uses only the mode prompt and ignores the base system prompt.
	ModePromptReplace ModePromptPriority = "replace"
)
```

Add to `ContextConfig`:

```go
ModePromptPriority ModePromptPriority `yaml:"mode_prompt_priority"`
```

Set default in `DefaultConfig`:

```go
Context: ContextConfig{
    // ... existing fields ...
    ModePromptPriority: ModePromptAppend,
},
```

- [ ] **Step 2: Add sample config value**

In `configs/lcoder.yaml`, under `context:`, add:

```yaml
context:
  # append (default) | prepend | replace
  mode_prompt_priority: append
```

- [ ] **Step 3: Commit**

```bash
git add pkg/config/config.go configs/lcoder.yaml
git commit -m "feat(config): mode prompt priority enum

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 2: Apply Priority in Context Manager Setup

**Files:**
- Modify: `pkg/agentsetup/setup.go`
- Modify: `pkg/contextmgr/manager.go`
- Modify: `pkg/agent/loop.go`
- Modify: `cmd/lcoder/main.go:236`
- Modify: `test/integration/agent_realrun_test.go:363`
- Modify: `test/integration/parallel_tools_test.go:246`
- Modify: `pkg/agentsetup/setup_test.go:37` and `:61`

- [ ] **Step 1: Pass priority into NewContextManager**

Change `NewContextManager` signature:

```go
func NewContextManager(
	cfg config.Config,
	budget config.TokenBudget,
	llmClient *llm.Client,
	contextText, skillsBlock string,
	activeMessages []models.AgentMessage,
	modePromptPriority config.ModePromptPriority,
) *contextmgr.Manager
```

Store the priority on the manager via a new option or field. Add to `pkg/contextmgr/manager.go`:

```go
// WithModePromptPriority sets how mode-specific prompt is ordered against the
// base system prompt.
func WithModePromptPriority(p config.ModePromptPriority) Option {
	return func(m *Manager) { m.modePromptPriority = p }
}
```

Add field to `Manager`:

```go
modePromptPriority config.ModePromptPriority
```

- [ ] **Step 2: Modify applyMode to respect priority**

In `pkg/agent/loop.go`, change `applyMode` to accept the manager and use the priority when building the mode block and system parts.

Replace the existing mode block construction:

```go
modeBlock := contextmgr.NewBlock(contextmgr.BlockMode, "mode", contextmgr.StabilityStable, 90,
    models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: "# Mode: " + mode.Name + "\n\n" + mode.SystemPrompt}))

switch a.contextManager.modePromptPriority {
case config.ModePromptReplace:
    // Replace base system prompt with mode prompt.
    a.contextManager.SetBlock(modeBlock)
    systemParts = []string{"# Mode: " + mode.Name + "\n\n" + mode.SystemPrompt}
case config.ModePromptPrepend:
    // Mode prompt comes before base system prompt.
    modeBlock.Priority = 110 // higher than base system block (100)
    a.contextManager.SetBlock(modeBlock)
    systemParts = append([]string{"# Mode: " + mode.Name + "\n\n" + mode.SystemPrompt}, systemParts...)
default: // ModePromptAppend
    a.contextManager.SetBlock(modeBlock)
    systemParts = append(systemParts, "# Mode: "+mode.Name+"\n\n"+mode.SystemPrompt)
}
```

Ensure the base system block still exists unless `ModePromptReplace` is selected.

- [ ] **Step 3: Update call sites**

Update each `agentsetup.NewContextManager` call to pass `cfg.Context.ModePromptPriority`:

`cmd/lcoder/main.go:236`:
```go
mgr := agentsetup.NewContextManager(cfg, budget, llmClient, contextText, skillsBlock, sess.ActiveMessages(), cfg.Context.ModePromptPriority)
```

`test/integration/agent_realrun_test.go:363`:
```go
mgr := agentsetup.NewContextManager(cfg, cfgBudget, client, contextText, skillsBlock, nil, cfg.Context.ModePromptPriority)
```

`test/integration/parallel_tools_test.go:246`:
```go
mgr := agentsetup.NewContextManager(cfg, cfgBudget, client, "", "", nil, cfg.Context.ModePromptPriority)
```

`pkg/agentsetup/setup_test.go:37` and `:61`:
```go
mgr := NewContextManager(cfg, config.TokenBudget{MaxTotal: 100000, TargetTotal: 90000, ReserveOutput: 8192}, nil, "project context here", "skill block here", nil, cfg.Context.ModePromptPriority)
```

- [ ] **Step 4: Commit**

```bash
git add pkg/agentsetup/setup.go pkg/contextmgr/manager.go pkg/agent/loop.go cmd/lcoder/main.go test/integration/agent_realrun_test.go test/integration/parallel_tools_test.go pkg/agentsetup/setup_test.go
git commit -m "feat(agent): apply mode prompt priority config

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 3: Test Mode Prompt Priority

**Files:**
- Create: `pkg/agentsetup/setup_mode_priority_test.go`

- [ ] **Step 1: Write failing tests**

Create `pkg/agentsetup/setup_mode_priority_test.go`:

```go
package agentsetup

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/models"
)

func TestModePromptPriority_Append(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{})
	mgr.SetBlock(contextmgr.NewBlock(contextmgr.BlockSystem, "system", contextmgr.StabilityStatic, 100,
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: "BASE"})))
	setModeBlock(mgr, "MODE", config.ModePromptAppend)

	text := mgr.Blocks()[0].Text() + "|" + mgr.Blocks()[1].Text()
	if !strings.HasPrefix(text, "BASE|") || !strings.HasSuffix(text, "|MODE") {
		t.Fatalf("expected BASE then MODE, got %q", text)
	}
}

func TestModePromptPriority_Prepend(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{})
	mgr.SetBlock(contextmgr.NewBlock(contextmgr.BlockSystem, "system", contextmgr.StabilityStatic, 100,
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: "BASE"})))
	setModeBlock(mgr, "MODE", config.ModePromptPrepend)

	text := mgr.Blocks()[0].Text() + "|" + mgr.Blocks()[1].Text()
	if !strings.HasPrefix(text, "MODE|") || !strings.HasSuffix(text, "|BASE") {
		t.Fatalf("expected MODE then BASE, got %q", text)
	}
}

func TestModePromptPriority_Replace(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{})
	mgr.SetBlock(contextmgr.NewBlock(contextmgr.BlockSystem, "system", contextmgr.StabilityStatic, 100,
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: "BASE"})))
	setModeBlock(mgr, "MODE", config.ModePromptReplace)

	blocks := mgr.Blocks()
	if len(blocks) != 1 || blocks[0].Text() != "MODE" {
		t.Fatalf("expected only MODE block, got %+v", blocks)
	}
}

func setModeBlock(mgr *contextmgr.Manager, text string, priority config.ModePromptPriority) {
	// mirror production logic from applyMode
	modeBlock := contextmgr.NewBlock(contextmgr.BlockMode, "mode", contextmgr.StabilityStable, 90,
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: text}))
	switch priority {
	case config.ModePromptReplace:
		mgr.SetBlock(modeBlock)
	case config.ModePromptPrepend:
		modeBlock.Priority = 110
		mgr.SetBlock(modeBlock)
	default:
		mgr.SetBlock(modeBlock)
	}
}
```

Run:

```bash
go test ./pkg/agentsetup/ -run TestModePromptPriority -v
```

Expected: FAIL — `setModeBlock` and tests need the real `applyMode` logic or a helper in the package.

- [ ] **Step 2: Add exported helper**

Add to `pkg/agentsetup/setup.go`:

```go
// SetModeBlock applies a mode-specific prompt block respecting the configured
// priority. It is exported for tests.
func SetModeBlock(mgr *contextmgr.Manager, mode config.ModeConfig, priority config.ModePromptPriority) {
	if mode.SystemPrompt == "" {
		return
	}
	text := "# Mode: " + mode.Name + "\n\n" + mode.SystemPrompt
	modeBlock := contextmgr.NewBlock(contextmgr.BlockMode, "mode", contextmgr.StabilityStable, 90,
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: text}))

	switch priority {
	case config.ModePromptReplace:
		// remove base system block
		mgr.SetBlock(contextmgr.NewBlock(contextmgr.BlockSystem, "system", contextmgr.StabilityStatic, 100,
			models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: ""})))
		mgr.SetBlock(modeBlock)
	case config.ModePromptPrepend:
		modeBlock.Priority = 110
		mgr.SetBlock(modeBlock)
	default:
		mgr.SetBlock(modeBlock)
	}
}
```

Update tests to call `SetModeBlock`.

Run:

```bash
go test ./pkg/agentsetup/ -run TestModePromptPriority -v
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/agentsetup/setup_mode_priority_test.go pkg/agentsetup/setup.go
git commit -m "test(agentsetup): verify mode prompt priority ordering

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 4: Document Precedence

**Files:**
- Create: `docs/modes-and-system-prompt.md`

- [ ] **Step 1: Write documentation**

Create `docs/modes-and-system-prompt.md`:

```markdown
# Modes and System Prompt Precedence

Lcoder combines two sources of system instructions:

1. **Base system prompt** — loaded from `configs/agents/system.txt` (or the user config).
2. **Mode-specific prompt** — loaded from `configs/agents/<mode>.yaml` under `system_prompt`.

## Priority

The `context.mode_prompt_priority` setting controls how they are ordered:

- `append` (default): base system prompt first, then mode prompt.
- `prepend`: mode prompt first, then base system prompt.
- `replace`: only the mode prompt is sent; the base system prompt is ignored.

## When to use each

- Use `append` when the base prompt contains universal rules and the mode prompt adds task-specific focus.
- Use `prepend` when a mode must override a universal rule that later instructions would otherwise contradict.
- Use `replace` for fully self-contained modes that should not inherit the default persona.

## Example

```yaml
context:
  mode_prompt_priority: prepend
```
```

- [ ] **Step 2: Commit**

```bash
git add docs/modes-and-system-prompt.md
git commit -m "docs: explain system prompt and mode priority

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 5: Full Verification

- [ ] **Step 1: Run tests**

```bash
go test ./pkg/agentsetup/... ./pkg/agent/... ./pkg/config/... -count=1
```
Expected: PASS.

- [ ] **Step 2: Build and vet**

```bash
go build ./...
go vet ./pkg/agentsetup/... ./pkg/agent/... ./pkg/config/... ./pkg/contextmgr/...
```
Expected: no output.

---

## Self-review

1. **Spec coverage:**
   - Config enum: Task 1
   - Runtime ordering: Task 2
   - Tests: Task 3
   - Docs: Task 4

2. **Placeholder scan:** No TBD/TODO.

3. **Type consistency:** `config.ModePromptPriority` used in config, context manager option, and agent logic.
