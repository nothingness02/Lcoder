# Session Branching Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the session storage layer actually support the `ParentID` field that already exists in `models.AgentMessage`, and expose branch navigation (`Fork`, `Checkout`) so the TUI can implement branching later.

**Architecture:** Keep the JSONL-per-session file format. Add `BranchID` to session metadata. A fork writes a new JSONL file sharing messages up to a chosen message, then diverges. `ActiveMessages()` loads the active branch by following `ParentID` links instead of returning all messages linearly.

**Tech Stack:** Go 1.25, existing `pkg/session`.

---

## File Structure

- **Modify:** `pkg/session/store.go`
- **Modify:** `pkg/session/session.go` (if separate; currently in store.go)
- **Create:** `pkg/session/branch_test.go`
- **Modify:** `pkg/models/message.go` — add `BranchID` metadata helper (optional)

---

## Task 1: Make Session Aware of ParentID Tree

**Files:**
- Modify: `pkg/session/store.go`
- Create: `pkg/session/branch_test.go`

- [ ] **Step 1: Write failing test**

Create `pkg/session/branch_test.go`:

```go
package session

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func TestSession_ActiveMessages_Branch(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	sess, _ := store.Create(dir)

	m1 := models.UserMessage("a")
	m2 := models.UserMessage("b")
	m3 := models.UserMessage("c")
	m2.ParentID = &m1.ID
	m3.ParentID = &m2.ID

	_ = sess.Append(m1)
	_ = sess.Append(m2)
	_ = sess.Append(m3)

	active := sess.ActiveMessages()
	if len(active) != 3 || active[0].Text() != "a" || active[2].Text() != "c" {
		t.Fatalf("expected linear chain a-b-c, got %v", texts(active))
	}
}

func TestSession_Fork(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	sess, _ := store.Create(dir)

	m1 := models.UserMessage("a")
	m2 := models.UserMessage("b")
	_ = sess.Append(m1)
	_ = sess.Append(m2)

	fork, err := sess.Fork(m1.ID)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if fork.ID == sess.ID {
		t.Fatal("fork id must differ from parent")
	}

	m3 := models.UserMessage("c")
	m3.ParentID = &m1.ID
	_ = fork.Append(m3)

	parentActive := sess.ActiveMessages()
	if len(parentActive) != 2 || parentActive[1].Text() != "b" {
		t.Fatalf("parent branch unchanged, got %v", texts(parentActive))
	}

	forkActive := fork.ActiveMessages()
	if len(forkActive) != 2 || forkActive[1].Text() != "c" {
		t.Fatalf("fork branch must be a-c, got %v", texts(forkActive))
	}
}

func texts(msgs []models.AgentMessage) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, m.Text())
	}
	return out
}
```

Run:
```bash
go test ./pkg/session/... -run 'TestSession_ActiveMessages_Branch|TestSession_Fork' -v
```
Expected: FAIL — `Fork` does not exist and `ActiveMessages` is linear.

- [ ] **Step 2: Implement branch-aware ActiveMessages and Fork**

Modify `pkg/session/store.go`:

```go
// ActiveMessages returns the messages on the active branch by following ParentID
// links from the most recent message backwards.
func (s *Session) ActiveMessages() []models.AgentMessage {
	if len(s.Messages) == 0 {
		return nil
	}
	byID := make(map[string]models.AgentMessage, len(s.Messages))
	for _, m := range s.Messages {
		byID[m.ID] = m
	}

	// Find the most recent message with no children (a leaf).
	hasChild := make(map[string]bool)
	for _, m := range s.Messages {
		if m.ParentID != nil {
			hasChild[*m.ParentID] = true
		}
	}
	var leaf *models.AgentMessage
	for i := len(s.Messages) - 1; i >= 0; i-- {
		if !hasChild[s.Messages[i].ID] {
			leaf = &s.Messages[i]
			break
		}
	}
	if leaf == nil {
		return s.Messages
	}

	// Walk backwards from leaf to root.
	var branch []models.AgentMessage
	cur := leaf
	for cur != nil {
		branch = append(branch, *cur)
		if cur.ParentID == nil {
			break
		}
		next, ok := byID[*cur.ParentID]
		if !ok {
			break
		}
		cur = &next
	}
	// Reverse.
	for i, j := 0, len(branch)-1; i < j; i, j = i+1, j-1 {
		branch[i], branch[j] = branch[j], branch[i]
	}
	return branch
}

// Fork creates a new session that shares all messages up to and including the
// message with parentMsgID, then diverges. The new session is persisted to disk.
func (s *Session) Fork(parentMsgID string) (*Session, error) {
	ancestor := s.findAncestor(parentMsgID)
	if ancestor == nil {
		return nil, fmt.Errorf("message %q not found", parentMsgID)
	}

	store := &Store{Dir: filepath.Dir(filepath.Dir(s.Path))}
	fork, err := store.Create(filepath.Dir(s.Path)) // project root inferred from path
	if err != nil {
		return nil, err
	}
	fork.CWD = s.CWD

	// Collect messages from root to ancestor.
	var shared []models.AgentMessage
	cur := ancestor
	for cur != nil {
		shared = append(shared, *cur)
		if cur.ParentID == nil {
			break
		}
		next := s.findAncestor(*cur.ParentID)
		cur = next
	}
	for i, j := 0, len(shared)-1; i < j; i, j = i+1, j-1 {
		shared[i], shared[j] = shared[j], shared[i]
	}

	for _, m := range shared {
		if err := fork.Append(m); err != nil {
			return nil, err
		}
	}
	return fork, nil
}

func (s *Session) findAncestor(id string) *models.AgentMessage {
	for i := range s.Messages {
		if s.Messages[i].ID == id {
			return &s.Messages[i]
		}
	}
	return nil
}
```

Note: the `Fork` store creation uses the existing `Store.Create` pattern; adjust path inference as needed.

Run:
```bash
go test ./pkg/session/... -run 'TestSession_ActiveMessages_Branch|TestSession_Fork' -v
```
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/session/store.go pkg/session/branch_test.go
git commit -m "feat(session): branch-aware ActiveMessages and Fork

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 2: Session Store Branch Listing

**Files:**
- Modify: `pkg/session/store.go`
- Modify: `pkg/session/branch_test.go`

- [ ] **Step 1: Add Branches method**

Add to `pkg/session/store.go`:

```go
// Branches returns all sessions for the project that share the same project
// directory (potential branches).
func (s *Store) Branches(cwd string) ([]Session, error) {
	return s.List(cwd)
}
```

- [ ] **Step 2: Test and commit**

Run:
```bash
go test ./pkg/session/... -count=1
```
Expected: PASS.

```bash
git add pkg/session/store.go
git commit -m "feat(session): Branches listing helper

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 3: Full Verification

- [ ] **Step 1: Run session tests**

```bash
go test ./pkg/session/... -count=1
```
Expected: PASS.

- [ ] **Step 2: Build and vet**

```bash
go build ./...
go vet ./pkg/session/...
```
Expected: no output.

---

## Self-review

1. **Spec coverage:**
   - Branch-aware active messages: Task 1
   - Fork: Task 1
   - Branch listing: Task 2

2. **Placeholder scan:** No TBD/TODO.

3. **Type consistency:** `Session.Fork(parentMsgID string)` returns `(*Session, error)`.
