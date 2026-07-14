# Memory Matching and Dynamic Recall Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add dependency-free relevance ranking and per-turn dynamic recall to Lcoder's built-in file-based memory, bounded by a token budget and configurable via `config.yaml`.

**Architecture:** A new `memory.Injector` mirrors `codeindex.Injector`: it ranks entries with a `Ranker` interface, budgets results using the context manager's estimator, and writes a dynamic `BlockRetrieval` named `memory_recall` each turn. The existing `memory.Store` keeps the same file format but gains atomic writes and an in-memory cache. Static memory blocks are still injected at startup for the user profile and, when dynamic recall is disabled, for agent memory.

**Tech Stack:** Go 1.25, existing `pkg/contextmgr`, `pkg/models`, `pkg/config` (koanf), `github.com/stretchr/testify/require` for tests.

---

## File map

| File | Responsibility |
|---|---|
| `pkg/memory/rank.go` | `Ranker` interface + default Jaccard/keyword scorer. |
| `pkg/memory/rank_test.go` | Unit tests for the scorer. |
| `pkg/memory/store.go` | Existing store; adds atomic writes and entry cache. |
| `pkg/memory/store_test.go` | Tests for atomic writes and cache invalidation. |
| `pkg/memory/injector.go` | `Injector` that ranks, budgets, and writes a context block. |
| `pkg/memory/injector_test.go` | Tests for token budgeting and block writing. |
| `pkg/config/config.go` | Extend `MemoryConfig` with dynamic-recall fields. |
| `configs/lcoder.yaml` | Add commented defaults for new memory options. |
| `pkg/agentsetup/setup.go` | Conditionally skip static `BlockMemory` when dynamic recall is on. |
| `pkg/agent/loop.go` | Add `MemoryInjector` field and call `Prefetch` each turn. |
| `pkg/agent/builder.go` | Add `WithMemoryInjector` builder option. |
| `cmd/lcoder/main.go` | Construct `memory.Injector` and pass it to the agent builder. |

---

## Task 1: Add memory ranking algorithm

**Files:**
- Create: `pkg/memory/rank.go`
- Create: `pkg/memory/rank_test.go`

### Step 1: Write the failing test

Create `pkg/memory/rank_test.go`:

```go
package memory

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultRankerOrdersJaccard(t *testing.T) {
	r := NewDefaultRanker()
	entries := []string{
		"deployment uses kubernetes",
		"unit tests should be fast",
		"kubernetes deployment pipeline",
	}
	scores := r.Rank("kubernetes deployment", entries)
	require.Len(t, scores, 3)
	require.Equal(t, "kubernetes deployment pipeline", scores[0].Text)
	require.GreaterOrEqual(t, scores[0].Score, scores[1].Score)
	require.GreaterOrEqual(t, scores[1].Score, scores[2].Score)
}

func TestDefaultRankerFiltersByMinScore(t *testing.T) {
	r := NewDefaultRanker()
	scores := r.Rank("graphql", []string{" unrelated note", "graphql schema design"})
	require.Len(t, scores, 1)
	require.Equal(t, "graphql schema design", scores[0].Text)
}

func TestDefaultRankerExactBonus(t *testing.T) {
	r := NewDefaultRanker()
	a := r.Score("auth flow", "auth flow with oauth")
	b := r.Score("auth flow", "authentication service")
	require.Greater(t, a, b)
}

func TestDefaultRankerEmptyQuery(t *testing.T) {
	r := NewDefaultRanker()
	scores := r.Rank("", []string{"anything"})
	require.Empty(t, scores)
}
```

Run:

```bash
cd D:/code_practise/project/lab_pj/Lcoder
go test ./pkg/memory -run TestDefaultRanker -v
```

Expected: FAIL with undefined `NewDefaultRanker`, `Rank`, `Score`, `RankedEntry`.

### Step 2: Implement `pkg/memory/rank.go`

```go
package memory

import (
	"math"
	"strings"
	"unicode"
)

// RankedEntry pairs a memory entry with its relevance score.
type RankedEntry struct {
	Text  string
	Score float64
}

// Ranker scores memory entries against a query.
type Ranker interface {
	Score(query, entry string) float64
	Rank(query string, entries []string) []RankedEntry
}

// DefaultRanker is a dependency-free scorer using Jaccard + keyword bonuses.
type DefaultRanker struct {
	MinScore float64
}

// NewDefaultRanker creates a ranker with a default minimum score of 0.1.
func NewDefaultRanker() *DefaultRanker {
	return &DefaultRanker{MinScore: 0.1}
}

// WithMinScore sets the score threshold. Entries below it are discarded by Rank.
func (r *DefaultRanker) WithMinScore(min float64) *DefaultRanker {
	r.MinScore = min
	return r
}

func (r *DefaultRanker) Rank(query string, entries []string) []RankedEntry {
	if strings.TrimSpace(query) == "" {
		return nil
	}
	var out []RankedEntry
	for _, e := range entries {
		score := r.Score(query, e)
		if score < r.MinScore {
			continue
		}
		out = append(out, RankedEntry{Text: e, Score: score})
	}
	sortRanked(out)
	return out
}

func (r *DefaultRanker) Score(query, entry string) float64 {
	qt := tokenize(query)
	et := tokenize(entry)
	if len(qt) == 0 || len(et) == 0 {
		return 0
	}

	intersection := 0
	union := make(map[string]bool)
	qset := make(map[string]bool)
	for _, t := range qt {
		qset[t] = true
		union[t] = true
	}
	for _, t := range et {
		if qset[t] {
			intersection++
		}
		union[t] = true
	}

	jaccard := float64(intersection) / float64(len(union))

	lowerQ := strings.ToLower(query)
	lowerE := strings.ToLower(entry)
	exactBonus := 0.0
	if strings.Contains(lowerE, lowerQ) {
		exactBonus = 0.3
	}

	prefixBonus := 0.0
	uniqueQ := uniqueTokens(qt)
	uniqueE := uniqueTokens(et)
	for _, qw := range uniqueQ {
		for _, ew := range uniqueE {
			if strings.HasPrefix(ew, qw) && ew != qw {
				prefixBonus += 0.05
			}
		}
	}
	if prefixBonus > 0.2 {
		prefixBonus = 0.2
	}

	score := jaccard + exactBonus + prefixBonus
	if score > 1.0 {
		score = 1.0
	}
	return score
}

func sortRanked(entries []RankedEntry) {
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[j].Score > entries[i].Score {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}
}

func tokenize(s string) []string {
	s = strings.ToLower(s)
	return strings.FieldsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r)
	})
}

func uniqueTokens(tokens []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, t := range tokens {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}
```

Run:

```bash
go test ./pkg/memory -run TestDefaultRanker -v
```

Expected: PASS.

### Step 3: Commit

```bash
git add pkg/memory/rank.go pkg/memory/rank_test.go
git commit -m "feat(memory): add dependency-free Jaccard ranker

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 2: Add atomic writes and entry cache to Store

**Files:**
- Modify: `pkg/memory/store.go`
- Modify: `pkg/memory/store_test.go`

### Step 1: Write the failing test

Append to `pkg/memory/store_test.go`:

```go
func TestStoreAtomicWriteUsesTempFile(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root)
	require.NoError(t, err)

	require.NoError(t, store.Add(MemoryTarget, "first entry"))

	path := store.globalPath(MemoryTarget)
	_, err = os.Stat(path + ".tmp")
	require.True(t, os.IsNotExist(err), "temporary file should be removed after atomic write")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "first entry")
}

func TestStoreCacheInvalidatedOnAdd(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root)
	require.NoError(t, err)

	require.NoError(t, store.Add(MemoryTarget, "alpha"))
	entries, err := store.GlobalEntries(MemoryTarget)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	require.NoError(t, store.Add(MemoryTarget, "beta"))
	entries, err = store.GlobalEntries(MemoryTarget)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Contains(t, entries, "beta")
}
```

Run:

```bash
go test ./pkg/memory -run TestStoreAtomicWriteUsesTempFile -v
```

Expected: FAIL because `globalPath` is unexported and cache fields do not exist.

### Step 2: Update `pkg/memory/store.go`

Add to imports: `"sync"`.

Add fields to `Store`:

```go
type Store struct {
	mu         sync.Mutex
	globalDir  string
	projectDir string
	limits     Limits
	cacheMu    sync.Mutex
	memoryCache []string
	userCache   []string
	cacheValid  bool
}
```

Change `saveFile` to atomic write:

```go
func (s *Store) saveFile(path string, t Target, entries []string) error {
	data := formatFile(targetName(t), entries, s.limitFor(t))
	tmp := path + ".tmp"
	if err := fsutil.WritePrivateFile(tmp, []byte(data)); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
```

Invalidate cache in `Add`, `Replace`, `Remove` after mutating entries and before save:

```go
func (s *Store) invalidateCache() {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.cacheValid = false
	s.memoryCache = nil
	s.userCache = nil
}
```

Add caching to `GlobalEntries`:

```go
func (s *Store) GlobalEntries(t Target) ([]string, error) {
	s.cacheMu.Lock()
	if s.cacheValid {
		var cached []string
		if t == MemoryTarget {
			cached = append([]string(nil), s.memoryCache...)
		} else {
			cached = append([]string(nil), s.userCache...)
		}
		s.cacheMu.Unlock()
		return cached, nil
	}
	s.cacheMu.Unlock()

	entries, err := s.loadFile(s.globalPath(t))
	if err != nil {
		return nil, err
	}

	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if t == MemoryTarget {
		s.memoryCache = append([]string(nil), entries...)
	} else {
		s.userCache = append([]string(nil), entries...)
	}
	s.cacheValid = true
	return entries, nil
}
```

`ProjectEntries` continues to call `loadFile` directly (project files are read-only).

Call `s.invalidateCache()` at the top of `Add`, `Replace`, and `Remove`.

Run:

```bash
go test ./pkg/memory -run 'TestStoreAtomicWriteUsesTempFile|TestStoreCacheInvalidatedOnAdd' -v
```

Expected: PASS.

### Step 3: Commit

```bash
git add pkg/memory/store.go pkg/memory/store_test.go
git commit -m "feat(memory): atomic writes and in-memory entry cache

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 3: Add memory Injector

**Files:**
- Create: `pkg/memory/injector.go`
- Create: `pkg/memory/injector_test.go`

### Step 1: Write the failing test

Create `pkg/memory/injector_test.go`:

```go
package memory

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/stretchr/testify/require"
)

func TestInjectorWritesRecallBlock(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	store, err := NewStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, store.Add(MemoryTarget, "Use Go 1.25 for this project"))
	require.NoError(t, store.Add(MemoryTarget, "Prefer kubernetes for deployment"))
	require.NoError(t, store.Add(MemoryTarget, "Never hardcode secrets"))

	inj := NewInjector(store, mgr, 1024)
	require.NoError(t, inj.Prefetch(context.Background(), "kubernetes deployment"))

	block, ok := mgr.GetBlock(contextmgr.BlockRetrieval, "memory_recall")
	require.True(t, ok)
	text := block.Text()
	require.Contains(t, text, "kubernetes")
	require.NotContains(t, text, "hardcode")
}

func TestInjectorBudgetsTokens(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	store, err := NewStore(t.TempDir())
	require.NoError(t, err)
	for i := 0; i < 10; i++ {
		require.NoError(t, store.Add(MemoryTarget, "kubernetes deployment note number"))
	}

	inj := NewInjector(store, mgr, 50)
	require.NoError(t, inj.Prefetch(context.Background(), "kubernetes"))

	block, ok := mgr.GetBlock(contextmgr.BlockRetrieval, "memory_recall")
	require.True(t, ok)
	// 50 tokens ~= 200 chars; should include at least one but not all ten.
	require.Less(t, len(block.Text()), 1000)
}

func TestInjectorClearsBlockWhenNoMatch(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	store, err := NewStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, store.Add(MemoryTarget, "unrelated fact"))

	inj := NewInjector(store, mgr, 1024)
	require.NoError(t, inj.Prefetch(context.Background(), "graphql"))

	block, ok := mgr.GetBlock(contextmgr.BlockRetrieval, "memory_recall")
	require.True(t, ok)
	require.Empty(t, block.Text())
}
```

Run:

```bash
go test ./pkg/memory -run TestInjector -v
```

Expected: FAIL with undefined `NewInjector`, `Prefetch`.

### Step 2: Implement `pkg/memory/injector.go`

```go
package memory

import (
	"context"
	"fmt"
	"strings"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/models"
)

// Injector prefetches relevant memory entries into the context manager each turn.
type Injector struct {
	store     *Store
	manager   *contextmgr.Manager
	ranker    Ranker
	maxTokens int
}

// NewInjector creates an injector bound to a store and context manager.
// maxTokens <= 0 defaults to 1024.
func NewInjector(store *Store, mgr *contextmgr.Manager, maxTokens int) *Injector {
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	return &Injector{
		store:     store,
		manager:   mgr,
		ranker:    NewDefaultRanker(),
		maxTokens: maxTokens,
	}
}

// WithRanker replaces the default ranker. Useful for tests or future providers.
func (inj *Injector) WithRanker(r Ranker) *Injector {
	inj.ranker = r
	return inj
}

// Prefetch ranks memory entries against query and writes a memory_recall block.
func (inj *Injector) Prefetch(ctx context.Context, query string) error {
	entries, err := inj.store.allEntries(MemoryTarget)
	if err != nil {
		inj.setBlock("")
		return fmt.Errorf("load memory entries: %w", err)
	}

	ranked := inj.ranker.Rank(query, entries)
	selected := inj.budgetResults(ranked)

	text := strings.Join(selected, "\n\n")
	if text != "" {
		text = fmt.Sprintf("// Recalled memory for query %q\n\n%s", query, text)
	}
	inj.setBlock(text)
	return nil
}

func (inj *Injector) budgetResults(ranked []RankedEntry) []string {
	if len(ranked) == 0 {
		return nil
	}
	estimator := inj.manager.Estimator()
	used := 0
	var selected []string
	for _, r := range ranked {
		msg := models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: r.Text})
		cost := estimator([]models.AgentMessage{msg})
		if used+cost > inj.maxTokens && len(selected) > 0 {
			break
		}
		selected = append(selected, r.Text)
		used += cost
	}
	return selected
}

func (inj *Injector) setBlock(text string) {
	block := contextmgr.NewBlockWithCacheHint(
		contextmgr.BlockRetrieval,
		"memory_recall",
		contextmgr.StabilityDynamic,
		60,
		contextmgr.CacheHintSkip,
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: text}),
	)
	inj.manager.SetBlock(block)
}
```

`budgetResults` compares the estimator's token cost directly against `maxTokens`.

Run:

```bash
go test ./pkg/memory -run TestInjector -v
```

Expected: PASS.

### Step 3: Commit

```bash
git add pkg/memory/injector.go pkg/memory/injector_test.go
git commit -m "feat(memory): add per-turn dynamic recall injector

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 4: Extend config with dynamic-recall settings

**Files:**
- Modify: `pkg/config/config.go`
- Modify: `configs/lcoder.yaml`

### Step 1: Write the failing test

Append to `pkg/config/config_test.go` (create if it does not exist):

```go
func TestMemoryDefaults(t *testing.T) {
	cfg := DefaultConfig()
	require.True(t, cfg.Memory.Enabled)
	require.True(t, cfg.Memory.DynamicRecall)
	require.Equal(t, 1024, cfg.Memory.RecallMaxTokens)
	require.InDelta(t, 0.1, cfg.Memory.RecallMinScore, 0.001)
}
```

Run:

```bash
go test ./pkg/config -run TestMemoryDefaults -v
```

Expected: FAIL because fields do not exist.

### Step 2: Modify `pkg/config/config.go`

Change `MemoryConfig`:

```go
// MemoryConfig controls persistent memory behavior.
type MemoryConfig struct {
	Enabled          bool    `yaml:"enabled"`
	MemoryCharLimit  int     `yaml:"memory_char_limit"`
	UserCharLimit    int     `yaml:"user_char_limit"`
	DynamicRecall    bool    `yaml:"dynamic_recall"`
	RecallMaxTokens  int     `yaml:"recall_max_tokens"`
	RecallMinScore   float64 `yaml:"recall_min_score"`
}
```

Update `DefaultConfig()` (find it and add the new fields):

```go
Memory: MemoryConfig{
	Enabled:         true,
	MemoryCharLimit: DefaultMemoryCharLimit,
	UserCharLimit:   DefaultUserCharLimit,
	DynamicRecall:   true,
	RecallMaxTokens: 1024,
	RecallMinScore:  0.1,
},
```

### Step 3: Modify `configs/lcoder.yaml`

Under the `memory:` block add:

```yaml
memory:
  enabled: true
  dynamic_recall: true        # per-turn relevance recall; set false for static snapshot
  recall_max_tokens: 1024     # token budget for recalled memory each turn
  recall_min_score: 0.1       # minimum relevance score (0..1) for an entry to be recalled
  # memory_char_limit: 2200
  # user_char_limit: 1375
```

Run:

```bash
go test ./pkg/config -run TestMemoryDefaults -v
```

Expected: PASS.

### Step 4: Commit

```bash
git add pkg/config/config.go configs/lcoder.yaml pkg/config/config_test.go
git commit -m "feat(config): add dynamic memory recall options

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 5: Conditionally skip static memory block

**Files:**
- Modify: `pkg/agentsetup/setup.go`

### Step 1: Update `NewContextManager`

Locate the memory block insertion in `pkg/agentsetup/setup.go`:

```go
if memStore != nil && cfg.Memory.Enabled {
    if userText, err := memStore.UserText(); err == nil && userText != "" {
        mgr.SetBlock(contextmgr.NewBlockWithCacheHint(contextmgr.BlockUserProfile, "user_profile", contextmgr.StabilityStable, 70, contextmgr.CacheHintBreakpoint,
            models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: userText})))
    }
    if memoryText, err := memStore.MemoryText(); err == nil && memoryText != "" {
        mgr.SetBlock(contextmgr.NewBlockWithCacheHint(contextmgr.BlockMemory, "memory", contextmgr.StabilityStable, 75, contextmgr.CacheHintBreakpoint,
            models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: memoryText})))
    }
}
```

Change it to:

```go
if memStore != nil && cfg.Memory.Enabled {
	if userText, err := memStore.UserText(); err == nil && userText != "" {
		mgr.SetBlock(contextmgr.NewBlockWithCacheHint(contextmgr.BlockUserProfile, "user_profile", contextmgr.StabilityStable, 70, contextmgr.CacheHintBreakpoint,
			models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: userText})))
	}
	if !cfg.Memory.DynamicRecall {
		if memoryText, err := memStore.MemoryText(); err == nil && memoryText != "" {
			mgr.SetBlock(contextmgr.NewBlockWithCacheHint(contextmgr.BlockMemory, "memory", contextmgr.StabilityStable, 75, contextmgr.CacheHintBreakpoint,
				models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: memoryText})))
		}
	}
}
```

### Step 2: Verify build

```bash
go build ./pkg/agentsetup/...
```

Expected: no errors.

### Step 3: Commit

```bash
git add pkg/agentsetup/setup.go
git commit -m "feat(agentsetup): skip static memory block when dynamic recall is enabled

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 6: Wire MemoryInjector into agent

**Files:**
- Modify: `pkg/agent/loop.go`
- Modify: `pkg/agent/builder.go`

### Step 1: Add field and builder option

In `pkg/agent/builder.go`, add after `WithContextSnapshotRecorder`:

```go
// WithMemoryInjector sets the per-turn memory prefetch injector.
func (b *Builder) WithMemoryInjector(inj MemoryInjector) *Builder {
	b.cfg.MemoryInjector = inj
	return b
}
```

### Step 2: Add type and field

In `pkg/agent/loop.go`, add to imports:

```go
"github.com/lcoder/lcoder/pkg/memory"
```

Add type alias near the top:

```go
// MemoryInjector recalls relevant memories before each turn.
type MemoryInjector interface {
	Prefetch(ctx context.Context, query string) error
}
```

Add to `Config`:

```go
// MemoryInjector prefetches relevant memory entries each turn.
MemoryInjector MemoryInjector
```

Add to `Agent`:

```go
memoryInjector MemoryInjector
```

### Step 3: Initialize in New

In `func New(...) *Agent`, set:

```go
ag.memoryInjector = cfg.MemoryInjector
```

### Step 4: Call Prefetch each turn

In `Agent.run`, after `a.refreshEphemeralReminders()` and before `a.maybeCompact(...)`, add:

```go
if a.memoryInjector != nil {
	if userText := lastUserText(a.mgr.AllMessages()); userText != "" {
		if err := a.memoryInjector.Prefetch(ctx, userText); err != nil {
			a.emit(ctx, events.ErrorEvent{
				Base:    events.Base{Type: events.Error, Turn: turn},
				Message: "memory prefetch: " + err.Error(),
			})
		}
	}
}
```

Add helper:

```go
func lastUserText(msgs []models.AgentMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == models.RoleUser {
			return msgs[i].Text()
		}
	}
	return ""
}
```

### Step 5: Verify build

```bash
go build ./pkg/agent/...
```

Expected: no errors.

### Step 6: Commit

```bash
git add pkg/agent/loop.go pkg/agent/builder.go
git commit -m "feat(agent): call memory injector before each turn

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 7: Construct and pass MemoryInjector in CLI

**Files:**
- Modify: `cmd/lcoder/main.go`

### Step 1: Create injector in prepareAgent

After `mgr := agentsetup.NewContextManager(...)` and before building the agent, add:

```go
var memoryInjector *memory.Injector
if memStore != nil && cfg.Memory.Enabled && cfg.Memory.DynamicRecall {
	memoryInjector = memory.NewInjector(memStore, mgr, cfg.Memory.RecallMaxTokens).
		WithRanker(memory.NewDefaultRanker().WithMinScore(cfg.Memory.RecallMinScore))
}
```

### Step 2: Pass to agent builder

In the `agent.NewBuilder()` chain, add:

```go
WithMemoryInjector(memoryInjector).
```

after `.WithContextSnapshotRecorder(contextSnapshotRecorder).`

### Step 3: Verify build

```bash
go build ./cmd/lcoder
```

Expected: no errors.

### Step 4: Commit

```bash
git add cmd/lcoder/main.go
git commit -m "feat(cli): wire memory injector into agent setup

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 8: Verification

### Step 1: Run memory package tests

```bash
go test ./pkg/memory/... -v
```

Expected: all tests pass.

### Step 2: Run agent and agentsetup tests

```bash
go test ./pkg/agent/... ./pkg/agentsetup/... ./pkg/config/...
```

Expected: all tests pass.

### Step 3: Run full suite

```bash
go test $(go list ./... | grep -v 'reference/Shannon')
```

Expected: all tests pass.

### Step 4: Vet

```bash
go vet $(go list ./... | grep -v 'reference/Shannon')
```

Expected: no issues.

### Step 5: Manual smoke test

Create a memory file and run a one-shot prompt:

```bash
mkdir -p ~/.lcoder/memory
echo -e "Use Go modules for dependency management\n§\nPrefer kubernetes for deployment" > ~/.lcoder/memory/MEMORY.md
go run ./cmd/lcoder -p "how should I deploy this?"
```

Expected: the deployment memory is recalled; unrelated entries are not.

### Step 6: Commit verification results (optional)

If any tests were added or fixed during verification, commit them. Otherwise no new commit is needed.

---

## Self-review checklist

- [ ] **Spec coverage:** Each design section (ranking, storage atomic/cache, injector, timing, size, config) maps to one or more tasks.
- [ ] **No placeholders:** Every step has concrete file paths, code, and commands.
- [ ] **Type consistency:** `MemoryInjector` interface, `Injector` struct, `Ranker` interface, and `MemoryConfig` fields match across all tasks.
- [ ] **Backwards compatibility:** `dynamic_recall: false` keeps existing static injection behavior; defaults are safe.
- [ ] **TDD:** Each task starts with a failing test.

Gaps: none identified.
