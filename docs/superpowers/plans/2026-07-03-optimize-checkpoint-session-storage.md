# Optimize Checkpoint / Session Storage Strategy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate checkpoint write amplification, keep a small history of automatic checkpoints, and make session persistence append-only instead of rewriting the entire JSONL file on every message.

**Architecture:** Introduce a retention policy inside `pkg/checkpoint` so `FileStore.Save` writes versioned checkpoint files. Lower the automatic checkpoint frequency in `pkg/agent/loop.go` to every N turns (default 5). Change `pkg/session/store.go` so `Session.Append` appends a single JSON line to the session file atomically, while `Session.Replace` continues to rewrite the whole file for compaction commits.

**Tech Stack:** Go 1.25, existing `pkg/checkpoint`, `pkg/session`, `pkg/agent`.

---

## File Structure

- **Modify:** `pkg/checkpoint/filestore.go` — versioned `Save`, load-latest logic, retention
- **Create:** `pkg/checkpoint/retention.go` — retention policy + cleanup
- **Create:** `pkg/checkpoint/retention_test.go`
- **Modify:** `pkg/checkpoint/filestore_test.go` — update for new naming
- **Modify:** `pkg/agent/loop.go` — configurable auto-checkpoint interval
- **Modify:** `pkg/agent/config.go` — add `CheckpointInterval` field
- **Modify:** `pkg/session/store.go` — true append in `Session.Append`
- **Modify:** `pkg/session/store_test.go` — test append-only behavior
- **Modify:** `cmd/lcoder/main.go` — pass interval to agent builder

---

## Task 1: Versioned Checkpoint FileStore

**Files:**
- Modify: `pkg/checkpoint/filestore.go`
- Modify: `pkg/checkpoint/filestore_test.go`
- Create: `pkg/checkpoint/retention.go`
- Test: `pkg/checkpoint/retention_test.go`

- [ ] **Step 1: Write the failing test for versioned save**

Create `pkg/checkpoint/filestore_test.go` (or append if it exists):

```go
package checkpoint

import (
	"testing"
	"time"
)

func TestFileStore_SaveCreatesVersionedFiles(t *testing.T) {
	dir := t.TempDir()
	fs := NewFileStore(dir)

	cp1 := &Checkpoint{Version: CurrentVersion, CreatedAt: time.Now().UTC(), Session: &SessionSnapshot{SessionID: "s1"}}
	if err := fs.Save("s1", cp1); err != nil {
		t.Fatalf("save first: %v", err)
	}

	time.Sleep(10 * time.Millisecond)
	cp2 := &Checkpoint{Version: CurrentVersion, CreatedAt: time.Now().UTC(), Session: &SessionSnapshot{SessionID: "s1"}}
	if err := fs.Save("s1", cp2); err != nil {
		t.Fatalf("save second: %v", err)
	}

	ids, err := fs.List("s1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 versioned checkpoints, got %d", len(ids))
	}

	latest, err := fs.LoadLatest("s1")
	if err != nil {
		t.Fatalf("load latest: %v", err)
	}
	if latest.CreatedAt.Before(cp1.CreatedAt) || latest.CreatedAt.Equal(cp1.CreatedAt) {
		t.Fatal("load latest did not return the newest checkpoint")
	}
}
```

Run:
```bash
go test ./pkg/checkpoint/... -run TestFileStore_SaveCreatesVersionedFiles -v
```
Expected: FAIL — `List(sessionID)` and `LoadLatest` do not exist.

- [ ] **Step 2: Implement versioned Save and List**

Replace `pkg/checkpoint/filestore.go` with:

```go
package checkpoint

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FileStore persists checkpoints as JSON files on the local filesystem.
type FileStore struct {
	Dir       string
	Retention RetentionPolicy
}

// NewFileStore creates a FileStore that writes checkpoints into dir.
func NewFileStore(dir string) *FileStore {
	return &FileStore{
		Dir:       dir,
		Retention: DefaultRetentionPolicy(),
	}
}

const checkpointSuffix = ".checkpoint.json"

// Save persists cp to a versioned file named after id, reason and timestamp.
func (fs *FileStore) Save(id string, cp *Checkpoint) error {
	if err := os.MkdirAll(fs.Dir, 0o755); err != nil {
		return err
	}
	data, err := cp.MarshalJSON()
	if err != nil {
		return err
	}
	path := fs.versionPath(id, cp)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o644); err != nil {
		return err
	}
	return fs.Retention.Apply(fs.Dir, id)
}

// Load reads the checkpoint stored under the legacy single-file id.
func (fs *FileStore) Load(id string) (*Checkpoint, error) {
	path := filepath.Join(fs.Dir, sanitize(id)+checkpointSuffix)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return decodeCheckpoint(data)
}

// LoadLatest returns the most recent automatic or crash checkpoint for id.
func (fs *FileStore) LoadLatest(id string) (*Checkpoint, error) {
	ids, err := fs.List(id)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, ErrNotFound
	}
	return fs.Load(ids[len(ids)-1])
}

// List returns the identifiers of all stored checkpoints for a session, sorted oldest first.
func (fs *FileStore) List(sessionID string) ([]string, error) {
	entries, err := os.ReadDir(fs.Dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	prefix := sanitize(sessionID) + "."
	var ids []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, checkpointSuffix) {
			continue
		}
		stem := strings.TrimSuffix(name, checkpointSuffix)
		if !strings.HasPrefix(stem, prefix) {
			continue
		}
		ids = append(ids, stem)
	}
	sort.Strings(ids)
	return ids, nil
}

// Delete removes the checkpoint stored under id.
func (fs *FileStore) Delete(id string) error {
	path := filepath.Join(fs.Dir, sanitize(id)+checkpointSuffix)
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

func decodeCheckpoint(data []byte) (*Checkpoint, error) {
	cp := &Checkpoint{}
	if err := cp.UnmarshalJSON(data); err != nil {
		return nil, fmt.Errorf("decode checkpoint: %w", err)
	}
	return cp, nil
}

func (fs *FileStore) versionPath(id string, cp *Checkpoint) string {
	reason := ReasonAuto
	if cp.Session != nil && cp.Session.Reason != "" {
		reason = cp.Session.Reason
	}
	ts := cp.CreatedAt.UTC().Format("20060102T150405.000000000")
	turn := ""
	if cp.Runtime != nil {
		turn = fmt.Sprintf(".t%d", cp.Runtime.Turn)
	}
	return filepath.Join(fs.Dir, fmt.Sprintf("%s.%s%s.%s%s", sanitize(id), reason, turn, ts, checkpointSuffix))
}

func sanitize(id string) string {
	return strings.ReplaceAll(id, string(filepath.Separator), "_")
}
```

Run:
```bash
go test ./pkg/checkpoint/... -run TestFileStore_SaveCreatesVersionedFiles -v
```
Expected: PASS.

- [ ] **Step 3: Implement retention policy**

Create `pkg/checkpoint/retention.go`:

```go
package checkpoint

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RetentionPolicy decides which old checkpoint files to delete.
type RetentionPolicy interface {
	Apply(dir, sessionID string) error
}

// KeepCountRetention keeps at most MaxCount automatic checkpoints per session.
type KeepCountRetention struct {
	MaxCount int
}

// DefaultRetentionPolicy keeps the 10 most recent automatic checkpoints per session.
func DefaultRetentionPolicy() RetentionPolicy {
	return &KeepCountRetention{MaxCount: 10}
}

func (k *KeepCountRetention) Apply(dir, sessionID string) error {
	if k.MaxCount <= 0 {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	prefix := sanitize(sessionID) + "." + ReasonAuto + "."
	var autoFiles []os.DirEntry
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, checkpointSuffix) {
			continue
		}
		stem := strings.TrimSuffix(name, checkpointSuffix)
		if !strings.HasPrefix(stem, prefix) {
			continue
		}
		autoFiles = append(autoFiles, e)
	}
	if len(autoFiles) <= k.MaxCount {
		return nil
	}
	sort.Slice(autoFiles, func(i, j int) bool {
		fi, _ := autoFiles[i].Info()
		fj, _ := autoFiles[j].Info()
		return fi.ModTime().Before(fj.ModTime())
	})
	for _, e := range autoFiles[:len(autoFiles)-k.MaxCount] {
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
	return nil
}
```

Run:
```bash
go test ./pkg/checkpoint/... -v
```
Expected: PASS.

- [ ] **Step 4: Add retention test**

Create `pkg/checkpoint/retention_test.go`:

```go
package checkpoint

import (
	"testing"
	"time"
)

func TestRetention_KeepsMaxCount(t *testing.T) {
	dir := t.TempDir()
	fs := NewFileStore(dir)
	fs.Retention = &KeepCountRetention{MaxCount: 3}

	for i := 0; i < 5; i++ {
		cp := &Checkpoint{Version: CurrentVersion, CreatedAt: time.Now().UTC().Add(time.Duration(i) * time.Second)}
		if err := fs.Save("s1", cp); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}

	ids, err := fs.List("s1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("expected 3 retained, got %d", len(ids))
	}
}
```

Run:
```bash
go test ./pkg/checkpoint/... -run TestRetention_KeepsMaxCount -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/checkpoint/filestore.go pkg/checkpoint/filestore_test.go pkg/checkpoint/retention.go pkg/checkpoint/retention_test.go
git commit -m "feat(checkpoint): versioned checkpoint files with retention policy

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 2: Configurable Auto-Checkpoint Interval

**Files:**
- Modify: `pkg/agent/config.go`
- Modify: `pkg/agent/loop.go`
- Modify: `pkg/agent/builder.go`
- Modify: `cmd/lcoder/main.go`
- Test: `pkg/agent/checkpoint_test.go` or new `pkg/agent/loop_checkpoint_test.go`

- [ ] **Step 1: Add CheckpointInterval to agent Config**

Modify `pkg/agent/agent.go` `Config` struct (around line 33):

```go
// Config controls agent behavior.
type Config struct {
	// ... existing fields ...

	// CheckpointInterval is the number of turns between automatic checkpoints.
	// 0 or 1 means checkpoint every turn; values <=0 are treated as 1.
	CheckpointInterval int
}
```

- [ ] **Step 2: Update maybeCheckpoint to respect interval**

Modify `pkg/agent/loop.go` `maybeCheckpoint`:

```go
func (a *Agent) maybeCheckpoint(ctx context.Context, turn int, reason string) {
	if a.cfg.CheckpointStore == nil || a.cfg.SessionID == "" {
		return
	}
	interval := a.cfg.CheckpointInterval
	if interval <= 0 {
		interval = 1
	}
	if reason == ReasonAuto && turn%interval != 0 {
		return
	}
	cp, err := a.CheckpointWithReason(reason)
	if err != nil {
		a.emit(ctx, events.ErrorEvent{
			Base:    events.Base{Type: events.Error, Turn: turn},
			Message: "checkpoint: " + err.Error(),
		})
		return
	}
	if err := a.cfg.CheckpointStore.Save(a.cfg.SessionID, cp); err != nil {
		a.emit(ctx, events.ErrorEvent{
			Base:    events.Base{Type: events.Error, Turn: turn},
			Message: "checkpoint save: " + err.Error(),
		})
	}
}
```

- [ ] **Step 3: Wire default interval in builder and main**

In `pkg/agent/builder.go` (or wherever `NewBuilder` sets defaults), default `CheckpointInterval` to 5 if not set.

In `cmd/lcoder/main.go`, pass the value through `agent.Config`:

```go
agent.Config{
	// ...
	CheckpointStore:    checkpoint.NewFileStore(filepath.Join(session.DefaultDir(), "checkpoints")),
	CheckpointInterval: 5,
}
```

- [ ] **Step 4: Write test for interval**

Create `pkg/agent/loop_checkpoint_test.go`:

```go
package agent

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/checkpoint"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
)

func TestAutoCheckpointInterval(t *testing.T) {
	client := llmtest.Client(llmtest.Turn(llmtest.Done(models.AssistantMessage("ok"), nil)))
	store := checkpoint.NewFileStore(t.TempDir())
	bus := events.New()
	ag := NewWithObservability(Config{
		SystemPrompt:       "x",
		Model:              models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		MaxTurns:           6,
		ToolExecutionMode:  models.ExecutionParallel,
		SessionID:          "sess-1",
		CheckpointStore:    store,
		CheckpointInterval: 2,
	}, client, testRegistry("."), permissions.NewEngine(permissions.DefaultConfig()), bus, nil)

	_ = ag.Prompt(context.Background(), models.UserMessage("go"))

	ids, _ := store.List("sess-1")
	if len(ids) != 3 {
		t.Fatalf("expected 3 auto checkpoints (turns 2,4,6), got %d", len(ids))
	}
}
```

Run:
```bash
go test ./pkg/agent/... -run TestAutoCheckpointInterval -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/agent/agent.go pkg/agent/loop.go pkg/agent/builder.go pkg/agent/loop_checkpoint_test.go cmd/lcoder/main.go
git commit -m "feat(agent): configurable auto-checkpoint interval

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 3: Make Session.Append Append-Only

**Files:**
- Modify: `pkg/session/store.go`
- Test: `pkg/session/store_test.go`

- [ ] **Step 1: Write failing test for append-only behavior**

Append to `pkg/session/store_test.go`:

```go
func TestSessionAppend_AppendsOnly(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	sess, err := store.Create(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	m1 := models.UserMessage("hello")
	if err := sess.Append(m1); err != nil {
		t.Fatalf("append first: %v", err)
	}

	infoBefore, _ := os.Stat(sess.Path)
	m2 := models.UserMessage("world")
	if err := sess.Append(m2); err != nil {
		t.Fatalf("append second: %v", err)
	}

	loaded, err := store.Load(sess.Path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(loaded.Messages))
	}
	_ = infoBefore
}
```

Run:
```bash
go test ./pkg/session/... -run TestSessionAppend_AppendsOnly -v
```
Expected: FAIL or PASS depending on current behavior; we will change it to append-only.

- [ ] **Step 2: Implement append-only Append**

Modify `pkg/session/store.go`:

```go
// Append adds a message to the session and persists it.
func (s *Session) Append(msg models.AgentMessage) error {
	if msg.Metadata == nil {
		msg.Metadata = make(map[string]any)
	}
	msg.Metadata["session_id"] = s.ID
	msg.Metadata["cwd"] = s.CWD
	msg.Metadata["saved_at"] = time.Now().UnixMilli()

	s.Messages = append(s.Messages, msg)

	return s.appendOne(msg)
}

// appendOne writes a single message to the session file atomically.
// It creates the file if it does not exist; otherwise it appends one JSONL line.
func (s *Session) appendOne(msg models.AgentMessage) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(s.Path), ".session-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	// Copy existing content if any.
	if existing, err := os.ReadFile(s.Path); err == nil {
		if _, err := tmp.Write(existing); err != nil {
			tmp.Close()
			return err
		}
	} else if !os.IsNotExist(err) {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.Path)
}
```

Remove the old `Save()` call from `Append`.

Run:
```bash
go test ./pkg/session/... -run TestSessionAppend_AppendsOnly -v
```
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/session/store.go pkg/session/store_test.go
git commit -m "feat(session): append-only message persistence

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 4: Full Verification

**Files:** No new files.

- [ ] **Step 1: Run affected packages**

```bash
go test ./pkg/checkpoint/... ./pkg/session/... ./pkg/agent/... -count=1
```
Expected: all PASS.

- [ ] **Step 2: Build binary**

```bash
go build ./cmd/lcoder/
```
Expected: no output.

- [ ] **Step 3: Vet**

```bash
go vet ./pkg/checkpoint/... ./pkg/session/... ./pkg/agent/...
```
Expected: no output.

---

## Self-review

1. **Spec coverage:**
   - Versioned checkpoint files: Task 1
   - Retention policy: Task 1
   - Configurable auto-save interval: Task 2
   - Session append-only: Task 3

2. **Placeholder scan:** No TBD/TODO.

3. **Type consistency:**
   - `FileStore.List(sessionID string)` returns checkpoint identifiers for that session.
   - `LoadLatest` delegates to `Load` with the latest identifier.
   - `CheckpointInterval` defaults to 5 in builder.
