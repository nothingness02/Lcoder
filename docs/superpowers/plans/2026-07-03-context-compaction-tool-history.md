# Context Compaction Tool History Preservation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let users choose a compaction strategy that preserves complete `tool_use`/`tool_result` pairs instead of blindly keeping the last N messages.

**Architecture:** Add `CompactionStrategy` to `config.ContextConfig` with values `keep_recent` (default, current behavior) and `preserve_tool_pairs`. Implement `compaction.PreserveToolPairs` strategy. Wire it into `contextmgr.Manager` via a new option so `foldOlder` uses the strategy to decide which messages to summarize and which to retain.

**Tech Stack:** Go 1.25, `pkg/compaction`, `pkg/contextmgr`, `pkg/config`.

---

## File Structure

- **Modify:** `pkg/config/config.go`
- **Modify:** `configs/lcoder.yaml`
- **Create:** `pkg/compaction/preserve_tool_pairs.go`
- **Create:** `pkg/compaction/preserve_tool_pairs_test.go`
- **Modify:** `pkg/contextmgr/manager.go`
- **Create:** `pkg/contextmgr/options.go`
- **Modify:** `pkg/agentsetup/setup.go`
- **Modify:** `cmd/lcoder/main.go`
- **Modify:** `cmd/lcoder/wiring.go`

---

## Task 1: Add Compaction Strategy Config

**Files:**
- Modify: `pkg/config/config.go`
- Modify: `configs/lcoder.yaml`

- [ ] **Step 1: Add enum and field**

In `pkg/config/config.go`, add:

```go
// CompactionStrategy selects how eager compaction folds older messages.
type CompactionStrategy string

const (
	// CompactionKeepRecent keeps the last N messages and summarizes the rest.
	CompactionKeepRecent CompactionStrategy = "keep_recent"
	// CompactionPreserveToolPairs keeps complete tool_use/tool_result pairs even
	// if they fall outside the recent tail.
	CompactionPreserveToolPairs CompactionStrategy = "preserve_tool_pairs"
)
```

Add to `ContextConfig`:

```go
CompactionStrategy CompactionStrategy `yaml:"compaction_strategy"`
```

Set default in `DefaultConfig`:

```go
Context: ContextConfig{
    // ... existing fields ...
    CompactionStrategy: CompactionKeepRecent,
},
```

Add validation in `ContextConfig.validate` (from the config-validation plan):

```go
switch c.CompactionStrategy {
case "", CompactionKeepRecent, CompactionPreserveToolPairs:
default:
    return fmt.Errorf("compaction_strategy must be %q or %q", CompactionKeepRecent, CompactionPreserveToolPairs)
}
```

- [ ] **Step 2: Add sample config**

In `configs/lcoder.yaml`, under `context:`:

```yaml
context:
  # keep_recent (default) | preserve_tool_pairs
  compaction_strategy: keep_recent
```

- [ ] **Step 3: Commit**

```bash
git add pkg/config/config.go configs/lcoder.yaml
git commit -m "feat(config): compaction strategy enum

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 2: Implement Preserve Tool Pairs Strategy

**Files:**
- Create: `pkg/compaction/preserve_tool_pairs.go`
- Create: `pkg/compaction/preserve_tool_pairs_test.go`

- [ ] **Step 1: Write failing tests**

Create `pkg/compaction/preserve_tool_pairs_test.go`:

```go
package compaction

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func TestPreserveToolPairs_KeepsCompletePairs(t *testing.T) {
	// Sequence ending with a tool_result whose matching tool_use would be summarized away.
	msgs := []models.AgentMessage{
		models.UserMessage("first"),
		models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{ID: "call_1", Name: "read"}),
		models.NewAgentMessage(models.RoleToolResult, models.ToolResultContent{ToolCallID: "call_1", Content: []models.ContentPart{models.TextContent{Text: "r1"}}}),
		models.UserMessage("second"),
	}

	strategy := NewPreserveToolPairs(1) // ask to keep only the last message
	out, err := strategy.Compact(msgs, SimpleSummarize)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}

	// Must extend retention to include matching tool_use, so retained tail is 2 messages.
	if len(out) != 3 {
		t.Fatalf("expected 1 summary + 2 retained messages, got %d", len(out))
	}
	if out[0].Role != models.RoleSystem {
		t.Fatalf("expected summary system message first, got %v", out[0].Role)
	}
	if toolCallID(out[1]) != "call_1" || toolResultID(out[2]) != "call_1" {
		t.Fatalf("expected retained pair for call_1, got %+v", out)
	}
}

func TestPreserveToolPairs_NoExtendWhenTailIsClean(t *testing.T) {
	msgs := []models.AgentMessage{
		models.UserMessage("old"),
		models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{ID: "call_1", Name: "read"}),
		models.NewAgentMessage(models.RoleToolResult, models.ToolResultContent{ToolCallID: "call_1", Content: []models.ContentPart{models.TextContent{Text: "r1"}}}),
		models.UserMessage("recent"),
	}

	strategy := NewPreserveToolPairs(1)
	out, err := strategy.Compact(msgs, SimpleSummarize)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 1 summary + 1 retained message, got %d", len(out))
	}
	if out[1].Text() != "recent" {
		t.Fatalf("expected retained user message, got %+v", out[1])
	}
}

func toolCallID(m models.AgentMessage) string {
	for _, p := range m.Content {
		if tc, ok := p.(models.ToolCallContent); ok {
			return tc.ID
		}
	}
	return ""
}

func toolResultID(m models.AgentMessage) string {
	for _, p := range m.Content {
		if tr, ok := p.(models.ToolResultContent); ok {
			return tr.ToolCallID
		}
	}
	return ""
}
```

Run:

```bash
go test ./pkg/compaction/ -run TestPreserveToolPairs -v
```

Expected: FAIL — `NewPreserveToolPairs` does not exist.

- [ ] **Step 2: Implement strategy**

Create `pkg/compaction/preserve_tool_pairs.go`:

```go
package compaction

import (
	"fmt"

	"github.com/lcoder/lcoder/pkg/models"
)

// PreserveToolPairs keeps the last N messages but extends the retained window
// backwards to include any tool_use whose matching tool_result would otherwise
// be split across the summary boundary.
type PreserveToolPairs struct {
	KeepCount int
}

// NewPreserveToolPairs creates a compaction strategy that preserves complete
// tool_use/tool_result pairs.
func NewPreserveToolPairs(keep int) *PreserveToolPairs {
	if keep < 1 {
		keep = 1
	}
	return &PreserveToolPairs{KeepCount: keep}
}

// Compact summarizes older messages and keeps complete recent pairs.
func (p *PreserveToolPairs) Compact(messages []models.AgentMessage, summarize SummarizeFunc) ([]models.AgentMessage, error) {
	if len(messages) <= p.KeepCount {
		return messages, nil
	}

	keep := p.KeepCount
	cut := len(messages) - keep

	// Extend the retained window backwards while the first retained message is a
	// tool_result whose matching tool_use lies on the older side of the boundary.
	for cut > 0 {
		firstRetained := messages[cut]
		if firstRetained.Role != models.RoleToolResult {
			break
		}
		found := false
		for i := cut - 1; i >= 0; i-- {
			if messages[i].Role == models.RoleAssistant && matchesToolResult(firstRetained, messages[i]) {
				keep += cut - i
				cut = i
				found = true
				break
			}
		}
		if !found {
			break
		}
	}

	older := messages[:cut]
	recent := messages[cut:]

	summaryText, err := summarize(older)
	if err != nil {
		return nil, fmt.Errorf("summarize: %w", err)
	}

	summary := models.NewAgentMessage(models.RoleSystem, models.TextContent{
		Text: fmt.Sprintf("[Summary of earlier conversation]\n\n%s", summaryText),
	})
	if summary.Metadata == nil {
		summary.Metadata = make(map[string]any)
	}
	summary.Metadata["compacted"] = true

	return append([]models.AgentMessage{summary}, recent...), nil
}

func matchesToolResult(result models.AgentMessage, assistant models.AgentMessage) bool {
	var resultID string
	for _, p := range result.Content {
		if tr, ok := p.(models.ToolResultContent); ok {
			resultID = tr.ToolCallID
			break
		}
	}
	if resultID == "" {
		return false
	}
	for _, tc := range assistant.ToolCalls() {
		if tc.ID == resultID {
			return true
		}
	}
	return false
}
```

Run:

```bash
go test ./pkg/compaction/ -run TestPreserveToolPairs -v
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/compaction/preserve_tool_pairs.go pkg/compaction/preserve_tool_pairs_test.go
git commit -m "feat(compaction): preserve complete tool_use/tool_result pairs

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 3: Wire Strategy into Context Manager

**Files:**
- Modify: `pkg/contextmgr/manager.go`
- Modify: `pkg/contextmgr/options.go` (create if missing)
- Modify: `pkg/agentsetup/setup.go`
- Modify: `cmd/lcoder/main.go`
- Modify: `cmd/lcoder/wiring.go`

- [ ] **Step 1: Add compaction strategy option**

In `pkg/contextmgr/manager.go`, add field:

```go
compactionStrategy config.CompactionStrategy
```

Create `pkg/contextmgr/options.go`:

```go
package contextmgr

import "github.com/lcoder/lcoder/pkg/config"

// WithCompactionStrategy sets the eager compaction strategy.
func WithCompactionStrategy(s config.CompactionStrategy) Option {
	return func(m *Manager) { m.compactionStrategy = s }
}
```

Add `github.com/lcoder/lcoder/pkg/config` to imports in `pkg/contextmgr/manager.go`.

- [ ] **Step 2: Modify foldOlder to use strategy**

In `pkg/contextmgr/manager.go`, change `foldOlder`:

```go
func (m *Manager) foldOlder(keep int) (bool, error) {
	if m.summarizer == nil {
		return false, nil
	}
	recent, ok := m.GetBlock(BlockRecent, "recent")
	if !ok || len(recent.Messages) == 0 {
		return false, nil
	}

	if keep < 1 {
		keep = 1
	}
	if keep > len(recent.Messages) {
		keep = len(recent.Messages)
	}

	var compacted []models.AgentMessage
	var err error
	switch m.compactionStrategy {
	case config.CompactionPreserveToolPairs:
		compacted, err = compaction.NewPreserveToolPairs(keep).Compact(recent.Messages, m.summarizer)
	default:
		compacted, err = compaction.NewKeepLastStrategy(keep).Compact(recent.Messages, m.summarizer)
	}
	if err != nil {
		return false, err
	}
	if len(compacted) == len(recent.Messages) {
		return false, nil
	}

	// foldOlder previously stripped orphan tool_results from tail; the strategies
	// already guarantee complete pairs, but keep the existing safety strip.
	m.ReplaceRecent(stripLeadingOrphanToolResults(compacted))
	return true, nil
}
```

Import `github.com/lcoder/lcoder/pkg/compaction` and `github.com/lcoder/lcoder/pkg/config` in `manager.go`.

- [ ] **Step 3: Wire config through setup**

In `pkg/agentsetup/setup.go`, add option:

```go
contextmgr.WithCompactionStrategy(cfg.Context.CompactionStrategy),
```

- [ ] **Step 4: Update transform context wiring**

In `cmd/lcoder/wiring.go`, change `makeTransformContext` to respect strategy:

```go
func makeTransformContext(keep int, strategy config.CompactionStrategy) agent.TransformContext {
	return func(ctx context.Context, messages []models.AgentMessage) ([]models.AgentMessage, error) {
		if len(messages) <= keep+1 {
			return messages, nil
		}
		if len(messages)%compactionInterval != 0 {
			return messages, nil
		}
		var s compaction.Strategy
		switch strategy {
		case config.CompactionPreserveToolPairs:
			s = compaction.NewPreserveToolPairs(keep)
		default:
			s = compaction.NewKeepLastStrategy(keep)
		}
		return s.Compact(messages, compaction.SimpleSummarize)
	}
}
```

Update the call site in `cmd/lcoder/main.go`:

```go
transform := makeTransformContext(compactionKeep, cfg.Context.CompactionStrategy)
```

- [ ] **Step 5: Commit**

```bash
git add pkg/contextmgr/manager.go pkg/contextmgr/options.go pkg/agentsetup/setup.go cmd/lcoder/wiring.go cmd/lcoder/main.go
git commit -m "feat(contextmgr): use configured compaction strategy in foldOlder

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 4: Full Verification

- [ ] **Step 1: Run compaction and context manager tests**

```bash
go test ./pkg/compaction/... ./pkg/contextmgr/... ./pkg/config/... -count=1
```
Expected: PASS.

- [ ] **Step 2: Run integration compaction test**

```bash
go test -tags integration ./test/integration -run TestCompactionMechanisms -v
```
Expected: PASS (no API key required).

- [ ] **Step 3: Build and vet**

```bash
go build ./...
go vet ./pkg/compaction/... ./pkg/contextmgr/... ./pkg/config/... ./cmd/lcoder/...
```
Expected: no output.

---

## Self-review

1. **Spec coverage:**
   - Config enum: Task 1
   - Preserve tool pairs strategy: Task 2
   - Wiring into eager compaction: Task 3
   - Tests: Task 2 + Task 4

2. **Placeholder scan:** No TBD/TODO.

3. **Type consistency:** `config.CompactionStrategy` used in config, compaction strategy constructors, context manager option, and wiring.
