# Memory Matching, Dynamic Recall and Bounded Injection Design

**Date:** 2026-07-14  
**Scope:** Built-in file-based memory (`pkg/memory`) only. External memory providers are out of scope for this change but the design leaves a clean seam for them later.  
**Status:** Design draft pending review.

## 1. Background and Goals

Lcoder currently persists memory in two plain-text channels (`MEMORY.md` and `USER.md`) using the `§` entry separator and hard character limits copied from Hermes. At session startup the entire content of both files is injected as static `contextmgr` blocks. This works for small, stable facts but has two problems:

1. **No relevance filtering.** Every entry is sent every turn, even when most entries are irrelevant to the current query. As memory grows it wastes context budget and distracts the model.
2. **No per-turn refresh.** Memories added or edited in the current session do not influence the running conversation until the next session starts.

Hermes solves this with a two-layer model:

- A **frozen built-in snapshot** injected into the system prompt at startup.
- A **dynamic per-turn prefetch** from external memory providers, appended to the user message.

This design ports the *dynamic prefetch* concept to Lcoder's built-in file store, while keeping the store format backward-compatible.

### Goals

- Add a lightweight, dependency-free relevance matching algorithm for built-in memory entries.
- Inject only the most relevant memories each turn, bounded by a token budget.
- Keep existing file format and character limits unchanged.
- Align the new component with the existing `pkg/codeindex/injector.go` pattern.
- Make the feature optional and configurable via `config.yaml`.

### Non-goals

- External memory provider plugins (Mem0, Honcho, Holographic, etc.). The seam is preserved but not implemented.
- Semantic/embeddings search. We stay dependency-free and CPU-only.
- Rewriting memory storage to SQLite or a vector DB.

## 2. Current State

`pkg/memory` provides:

- `Store` with global (`~/.lcoder/memory/{MEMORY,USER}.md`) and project (`.lcoder/memory/{MEMORY,USER}.md`) files.
- `§` separator, header/usage decoration, char limits (`DefaultMemoryCharLimit = 2200`, `DefaultUserCharLimit = 1375`).
- CRUD via `Add`, `Replace`, `Remove` on the global file only.
- `MemoryText()` / `UserText()` return the merged global+project text for injection.

`pkg/agentsetup/setup.go::NewContextManager` calls `memStore.UserText()` and `memStore.MemoryText()` once when the manager is built and installs them as stable blocks:

```go
BlockUserProfile  priority 70
BlockMemory       priority 75
```

`pkg/tools/builtin/memory.go` exposes a `memory` tool for add/replace/remove.

## 3. Proposed Architecture

Introduce a new component `pkg/memory/injector.go` that mirrors `pkg/codeindex/injector.go`:

```text
┌─────────────────┐      query/userText      ┌─────────────────────┐
│   Agent Loop    │ ───────────────────────▶ │  memory.Injector    │
│                 │                          │                     │
│                 │ ◀── sets BlockMemory ─── │  - Rank entries     │
│                 │    or BlockRetrieval     │  - Budget results   │
└─────────────────┘                          │  - Write context    │
                                             └─────────────────────┘
                                                       │
                                                       ▼
                                              ┌─────────────────┐
                                              │  memory.Store   │
                                              │  (existing)     │
                                              └─────────────────┘
```

### 3.1 New types

```go
// Ranker scores memory entries against a query.
type Ranker interface {
    Score(query string, entry string) float64
}

// Injector prefetches relevant memory entries into the context manager.
type Injector struct {
    store     *Store
    manager   *contextmgr.Manager
    ranker    Ranker
    maxTokens int
    minScore  float64
}

// NewInjector creates an injector. maxTokens <= 0 defaults to 1024.
func NewInjector(store *Store, mgr *contextmgr.Manager, maxTokens int) *Injector

// Prefetch ranks memory entries for query and writes a context block.
// It updates an existing memory_recall block or creates a new one.
func (inj *Injector) Prefetch(ctx context.Context, query string) error
```

### 3.2 Block naming

Two blocks are involved:

- `BlockMemory` / name `memory` — stable baseline. Still built in `NewContextManager` from `Store.MemoryText()` when `cfg.Memory.Enabled` is true. This preserves the "frozen snapshot" behavior for very small stores or when dynamic recall is disabled.
- `BlockRetrieval` / name `memory_recall` — dynamic per-turn recall. Written by `Injector.Prefetch`. It is a `StabilityDynamic` block with priority `60` so it sits below skills/project_docs but above recent messages.

When dynamic recall is enabled, `NewContextManager` may optionally skip the static `BlockMemory` block and let `Injector` own all memory context. The default is **enabled dynamic + keep static user profile**, because user profile entries are usually stable and small.

## 4. Matching Algorithm

We implement a dependency-free hybrid scorer in `pkg/memory/rank.go`.

### 4.1 Preprocessing

```go
func tokenize(s string) []string
```

- Lowercase.
- Split on unicode spaces and punctuation (`!"#$%&'()*+,-./:;<=>?@[\]^_{|}~`).
- Drop empty tokens and a small English stop-word set (`the`, `a`, `is`, `to`, `of`, `and`, `in`, `for`, `on`, `with`, `as`, `at`, `by`).
- Keep duplicates for TF-like signals.

### 4.2 Score function

For a query `q` and entry `e`:

```go
qt := tokenize(q)
et := tokenize(e)

intersection := len(qt ∩ et)
union        := len(qt ∪ et)

jaccard := 0.0
if union > 0 {
    jaccard = float64(intersection) / float64(union)
}

exactBonus := 0.0
if strings.Contains(strings.ToLower(e), strings.ToLower(q)) {
    exactBonus = 0.3
}

prefixBonus := 0.0
for _, qw := range unique(qt) {
    for _, ew := range unique(et) {
        if strings.HasPrefix(ew, qw) {
            prefixBonus += 0.05
        }
    }
}
prefixBonus = min(prefixBonus, 0.2)

score := min(1.0, jaccard + exactBonus + prefixBonus)
```

### 4.3 Filtering

- `minScore` default `0.1`. Entries below it are discarded.
- If no entry passes the threshold, the injector writes an empty block (effectively removing prior recall).

### 4.4 Future seam

`Ranker` is an interface. A later semantic provider can implement `Score` with embeddings and replace the default ranker without touching `Injector`.

## 5. Storage

The existing `Store` format is preserved. Two implementation improvements are added:

### 5.1 Atomic writes

`Store.saveFile` currently uses `fsutil.WritePrivateFile`. We change it to:

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

This matches Hermes's temp-file + `os.replace()` pattern and avoids partial writes on crash.

### 5.2 In-memory cache

`Store` caches parsed entries so `Injector.Prefetch` does not re-read and re-tokenize files every turn:

```go
type Store struct {
    // ... existing fields ...
    cacheMu      sync.Mutex
    memoryCache  []string
    userCache    []string
    cacheValid   bool
}
```

`Add`/`Replace`/`Remove` invalidate the cache. `GlobalEntries`/`ProjectEntries` repopulate it on first use after invalidation. The cache is process-local and intentionally simple; Lcoder is a single-user CLI.

## 6. Injection Timing

### 6.1 Startup / restore

`NewContextManager` continues to inject static blocks:

- `BlockUserProfile` from `Store.UserText()` (always, when enabled).
- `BlockMemory` from `Store.MemoryText()` (only when `cfg.Memory.DynamicRecall == false`).

This keeps a small, stable baseline available for prefix caching.

### 6.2 Per-turn prefetch

In `pkg/agent/loop.go`, before each assistant turn begins, call the injector with the latest user text:

```go
// in loop.go runTurn or equivalent location
if a.memoryInjector != nil {
    userText := extractLastUserText(messages)
    if err := a.memoryInjector.Prefetch(ctx, userText); err != nil {
        // Log but do not fail the turn. Memory recall is best-effort.
        a.logger.Warn("memory prefetch failed", "error", err)
    }
}
```

`Prefetch` reads entries, ranks them, budgets them, and calls `manager.SetBlock(...)` with the `memory_recall` block.

### 6.3 Tool writes

When the `memory` tool adds/replaces/removes an entry, the store invalidates its cache. The next turn's `Prefetch` naturally sees the new state.

### 6.4 Session boundaries

No extra work is needed. Dynamic recall blocks are not persisted to checkpoints or sessions; they are reconstructed on the first turn after restore.

## 7. Injection Size

### 7.1 Storage limits (unchanged)

- `MEMORY.md`: 2200 chars.
- `USER.md`: 1375 chars.

These prevent unbounded growth of the persisted store.

### 7.2 Per-turn recall budget

`Injector` uses token budget, not character budget, because it runs inside `contextmgr`:

```go
maxTokens := inj.maxTokens
if maxTokens <= 0 {
    maxTokens = 1024
}

estimator := inj.manager.Estimator()
used := 0
var selected []string
for _, r := range ranked {
    msg := models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: r.Text})
    cost := estimator([]models.AgentMessage{msg})
    if used+cost > maxTokens && len(selected) > 0 {
        break
    }
    selected = append(selected, r.Text)
    used += cost
}
```

Default `maxTokens` is `1024`. Configurable via:

```yaml
memory:
  enabled: true
  dynamic_recall: true
  recall_max_tokens: 1024
  recall_min_score: 0.1
```

### 7.3 Minimum results

If at least one entry scores above `minScore`, at least one result is included even if it slightly exceeds the token budget. This avoids silent empty recall.

## 8. Configuration

Extend `pkg/config` `MemoryConfig`:

```go
type MemoryConfig struct {
    Enabled          bool `koanf:"enabled"`
    DynamicRecall    bool `koanf:"dynamic_recall"`
    RecallMaxTokens  int  `koanf:"recall_max_tokens"`
    RecallMinScore   float64 `koanf:"recall_min_score"`
}
```

Defaults:

```yaml
memory:
  enabled: true
  dynamic_recall: true
  recall_max_tokens: 1024
  recall_min_score: 0.1
```

When `dynamic_recall: false`, behavior is identical to today.

## 9. Error Handling

- `Prefetch` never fails the turn. Errors are logged and the existing `memory_recall` block is left unchanged.
- Store read errors during `Prefetch` are treated as "no memories available" and the block is cleared.
- Atomic write failures in `Store` return errors to the `memory` tool so the model knows the write did not persist.

## 10. Testing Plan

### Unit tests in `pkg/memory`

- `TestRankerJaccard`: verify Jaccard ordering.
- `TestRankerExactBonus`: whole-query substring boosts score above partial matches.
- `TestRankerMinScoreFilter`: entries below threshold are discarded.
- `TestStoreAtomicWrite`: write + crash simulation via temp file assertion.
- `TestStoreCacheInvalidation`: `Add` clears cache; next read repopulates.
- `TestInjectorBudgetsTokens`: ranked list is truncated by token budget.
- `TestInjectorMinimumResult`: at least one result returned when threshold is met.
- `TestInjectorEmptyWhenNoMatch`: block is empty/cleared when nothing relevant.

### Integration tests in `test/integration`

- A new or existing test creates a project, writes memory entries, runs one turn with a specific query, and asserts that only the relevant memory appears in the assistant context.

### Manual verification

```bash
go test ./pkg/memory/...
go vet ./pkg/memory/...
go run ./cmd/lcoder -mode explore -query "what did we decide about X?"
```

## 11. Migration and Backwards Compatibility

- File format is unchanged. Existing `~/.lcoder/memory/*.md` and project files continue to work.
- Default config preserves today's behavior if `dynamic_recall` is omitted or set to `false`.
- New config fields have safe defaults.
- No database migrations or index builds are required.

## 12. Open Questions

1. Should the static `BlockMemory` be removed entirely when dynamic recall is enabled, or kept as a small "always-in" baseline? The proposal keeps it disabled by default under dynamic recall but this is configurable.
2. Should we add a simple TTL or "stale entry" mechanism? Out of scope for this change.
3. Should memory recall be triggered by tool results in addition to user text? Deferred; start with user text only.

## 13. Appendix: Hermes Mapping

| Hermes concept | Lcoder equivalent in this design |
|---|---|
| Built-in `MEMORY.md`/`USER.md` | `pkg/memory.Store` (preserved) |
| External provider `prefetch(query)` | `memory.Injector.Prefetch(query)` |
| Holographic Jaccard/keyword scoring | `memory.Ranker` default implementation |
| Per-turn `<memory-context>` block | `contextmgr.BlockRetrieval` named `memory_recall` |
| Provider token/char limits | `Injector.maxTokens` + `contextmgr.Estimator()` |
| Session lifecycle hooks | Not needed; dynamic block is rebuilt each turn |
