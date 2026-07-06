# Lcoder 持久记忆系统实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 Lcoder 增加跨会话持久记忆能力：全局/项目 `MEMORY.md` 与 `USER.md` 自动注入系统提示词，并提供 `memory` 内置工具让 agent 管理全局记忆。

**Architecture:** 新增 `pkg/memory` 包负责文件发现、条目解析、容量检查与读写；在 `contextmgr` 中新增 `memory`/`user_profile` 两个 stable system block；`agentsetup.NewContextManager` 接收 `*memory.Store` 并在启用时注入块；`cmd/lcoder/main.go` 在 memory 启用时构造 store 并注册 `memory` 工具。

**Tech Stack:** Go 1.25, koanf, 文件系统，现有 `contextmgr`/`tools` 框架。

---

## 文件结构

| 文件 | 类型 | 职责 |
|---|---|---|
| `pkg/memory/limits.go` | 新建 | 默认字符上限常量 |
| `pkg/memory/entry.go` | 新建 | 条目解析、序列化、子串匹配、重复检测 |
| `pkg/memory/store.go` | 新建 | 发现全局/项目记忆目录、读写 `MEMORY.md`/`USER.md`、容量检查 |
| `pkg/tools/builtin/memory.go` | 新建 | `memory` 内置工具实现 |
| `pkg/contextmgr/block.go` | 修改 | 新增 `BlockMemory`、`BlockUserProfile`，更新 `IsSystemBlock` 与 `DefaultBlockOrder` |
| `pkg/config/config.go` | 修改 | 新增 `MemoryConfig` 及默认值 |
| `pkg/agentsetup/setup.go` | 修改 | 构造 memory store，加载并注入 memory blocks |
| `cmd/lcoder/main.go` | 修改 | 启用时创建 store、注册 `memory` 工具 |
| `pkg/memory/entry_test.go` | 新建 | 解析、匹配、容量单元测试 |
| `pkg/memory/store_test.go` | 新建 | store 读写、合并、超限单元测试 |
| `pkg/tools/builtin/memory_test.go` | 新建 | 工具参数校验、写盘后读取、错误路径 |
| `pkg/contextmgr/block_test.go` | 新建 | 验证新 block 被识别为 system block 且顺序正确 |
| `pkg/agentsetup/setup_test.go` | 修改 | 验证 memory block 注入，同步更新 `NewContextManager` 调用 |
| `test/integration/agent_realrun_test.go` | 修改 | 同步更新 `NewContextManager` 调用 |
| `test/integration/parallel_tools_test.go` | 修改 | 同步更新 `NewContextManager` 调用 |
| `configs/lcoder.yaml` | 修改 | 示例配置增加 `memory:` 块 |

---

## Task 1: 增加配置类型与默认值

**Files:**
- Modify: `pkg/config/config.go`
- Test: `pkg/config/config_test.go`（已有）

- [ ] **Step 1: 新增 `MemoryConfig` 并嵌入 `Config`**

在 `pkg/config/config.go` 的 `SandboxConfig` 之后、`Config` 之前添加：

```go
// MemoryConfig controls persistent memory behavior.
type MemoryConfig struct {
	Enabled         bool `yaml:"enabled"`
	MemoryCharLimit int  `yaml:"memory_char_limit"`
	UserCharLimit   int  `yaml:"user_char_limit"`
}
```

在 `Config` 结构体中添加字段：

```go
Memory MemoryConfig `yaml:"memory"`
```

- [ ] **Step 2: 设置默认值**

在 `DefaultConfig()` 返回的 `Config` 字面量中增加：

```go
Memory: MemoryConfig{
	Enabled:         true,
	MemoryCharLimit: 0, // 0 表示使用 pkg/memory 默认值
	UserCharLimit:   0,
},
```

- [ ] **Step 3: 在 koanf 默认 provider 中注册 memory 字段**

在 `Load()` 的 `confmap.Provider` 调用中，给顶层 map 增加：

```go
"memory": map[string]any{
	"enabled":           cfg.Memory.Enabled,
	"memory_char_limit": cfg.Memory.MemoryCharLimit,
	"user_char_limit":   cfg.Memory.UserCharLimit,
},
```

- [ ] **Step 4: 运行配置测试**

```bash
go test ./pkg/config/... -run TestDefaultConfig -v
```

Expected: PASS（如该包无对应测试，仅确认编译通过）。

- [ ] **Step 5: Commit**

```bash
git add pkg/config/config.go
git commit -m "feat(memory): add MemoryConfig with defaults and koanf wiring"
```

---

## Task 2: 创建 `pkg/memory/limits.go`

**Files:**
- Create: `pkg/memory/limits.go`
- Test: `pkg/memory/limits_test.go`（可选，仅验证常量存在）

- [ ] **Step 1: 编写 `pkg/memory/limits.go`**

```go
// Package memory provides persistent file-based memory storage for Lcoder.
package memory

const (
	// DefaultMemoryCharLimit is the default character cap for the agent memory channel.
	DefaultMemoryCharLimit = 2200
	// DefaultUserCharLimit is the default character cap for the user profile channel.
	DefaultUserCharLimit = 1375
	// EntrySeparator is the line used to split memory entries on disk.
	EntrySeparator = "§"
)
```

- [ ] **Step 2: 编译检查**

```bash
go build ./pkg/memory/...
```

Expected: 无错误。

- [ ] **Step 3: Commit**

```bash
git add pkg/memory/limits.go
git commit -m "feat(memory): define default character limits and entry separator"
```

---

## Task 3: 创建 `pkg/memory/entry.go`

**Files:**
- Create: `pkg/memory/entry.go`
- Test: `pkg/memory/entry_test.go`

- [ ] **Step 1: 编写失败测试 `TestParseEntries` 和 `TestFindEntryIndex`**

```go
package memory

import (
	"strings"
	"testing"
)

func TestParseEntriesSkipsHeaderAndSplitsOnSeparator(t *testing.T) {
	input := `═══════════════════════════════════════
MEMORY [10% — 220/2,200 chars]
═══════════════════════════════════════
Project uses Go 1.25.
§
Always run go vet before push.
`
	entries := parseEntries(input)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", len(entries), entries)
	}
	if !strings.Contains(entries[0], "Go 1.25") {
		t.Fatalf("unexpected first entry: %q", entries[0])
	}
	if !strings.Contains(entries[1], "go vet") {
		t.Fatalf("unexpected second entry: %q", entries[1])
	}
}

func TestParseEntriesEmpty(t *testing.T) {
	if parseEntries("") != nil {
		t.Fatal("expected nil for empty input")
	}
	if parseEntries("   \n\n  ") != nil {
		t.Fatal("expected nil for whitespace-only input")
	}
}

func TestFindEntryIndexUniqueSubstring(t *testing.T) {
	entries := []string{"foo bar", "baz qux"}
	idx, err := findEntryIndex(entries, "baz")
	if err != nil || idx != 1 {
		t.Fatalf("expected idx=1, got %d err=%v", idx, err)
	}
}

func TestFindEntryIndexNoMatch(t *testing.T) {
	_, err := findEntryIndex([]string{"a", "b"}, "z")
	if err == nil {
		t.Fatal("expected error for no match")
	}
}

func TestFindEntryIndexAmbiguous(t *testing.T) {
	_, err := findEntryIndex([]string{"abc", "abcd"}, "ab")
	if err == nil {
		t.Fatal("expected error for ambiguous match")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
go test ./pkg/memory/... -run TestParseEntries -v
```

Expected: FAIL with undefined functions.

- [ ] **Step 3: 实现 `pkg/memory/entry.go`**

```go
package memory

import (
	"fmt"
	"strings"
)

// charCount returns the sum of entry text lengths (separators not counted).
func charCount(entries []string) int {
	n := 0
	for _, e := range entries {
		n += len(e)
	}
	return n
}

// parseEntries reads a memory file body and returns its entries.
// It ignores decorative header lines and splits on the § separator.
func parseEntries(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	parts := strings.Split(text, "\n"+EntrySeparator+"\n")
	var entries []string
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if i == 0 {
			p = stripHeader(p)
		}
		if p != "" {
			entries = append(entries, p)
		}
	}
	return entries
}

// stripHeader drops the decorative header lines (═══... and title/usage).
func stripHeader(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	skipped := false
	for _, l := range lines {
		if !skipped {
			trim := strings.TrimSpace(l)
			if trim == "" || strings.HasPrefix(trim, "═") ||
				strings.HasPrefix(trim, "MEMORY") || strings.HasPrefix(trim, "USER") {
				continue
			}
			skipped = true
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// joinEntries serializes entries with the § separator.
func joinEntries(entries []string) string {
	if len(entries) == 0 {
		return ""
	}
	return strings.Join(entries, "\n"+EntrySeparator+"\n")
}

// formatFile produces the on-disk representation including header.
func formatFile(title string, entries []string, limit int) string {
	usage := charCount(entries)
	pct := 0
	if limit > 0 {
		pct = usage * 100 / limit
	}
	header := fmt.Sprintf("═══════════════════════════════════════\n%s [%d%% — %d/%d chars]\n═══════════════════════════════════════\n", title, pct, usage, limit)
	body := joinEntries(entries)
	if body == "" {
		return header
	}
	return header + body
}

// findEntryIndex returns the unique entry index containing substring.
func findEntryIndex(entries []string, substring string) (int, error) {
	if substring == "" {
		return -1, fmt.Errorf("old_text cannot be empty")
	}
	var matches []int
	for i, e := range entries {
		if strings.Contains(e, substring) {
			matches = append(matches, i)
		}
	}
	switch len(matches) {
	case 0:
		return -1, fmt.Errorf("old_text %q did not match any entry", substring)
	case 1:
		return matches[0], nil
	default:
		return -1, fmt.Errorf("old_text %q matched %d entries; provide a more specific substring", substring, len(matches))
	}
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
go test ./pkg/memory/... -run 'TestParseEntries|TestFindEntryIndex' -v
```

Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add pkg/memory/entry.go pkg/memory/entry_test.go
git commit -m "feat(memory): implement entry parsing, matching and formatting"
```

---

## Task 4: 创建 `pkg/memory/store.go`

**Files:**
- Create: `pkg/memory/store.go`
- Test: `pkg/memory/store_test.go`

- [ ] **Step 1: 编写失败测试**

```go
package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreReadsGlobalAndProjectEntries(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(filepath.Join(home, ".lcoder", "memory"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".lcoder", "memory"), 0750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	if err := os.WriteFile(filepath.Join(home, ".lcoder", "memory", "MEMORY.md"), []byte("global note"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".lcoder", "memory", "MEMORY.md"), []byte("project note"), 0640); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	text, err := store.MemoryText()
	if err != nil {
		t.Fatal(err)
	}
	if !containsInOrder(text, "global note", "project note") {
		t.Fatalf("expected merged memory text, got:\n%s", text)
	}
}

func TestStoreAddRespectsLimit(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(filepath.Join(home, ".lcoder", "memory"), 0750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	store, err := NewStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	store.WithLimits(Limits{MemoryCharLimit: 10})
	if err := store.Add(MemoryTarget, "short"); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(MemoryTarget, "this is way too long"); err == nil {
		t.Fatal("expected limit error")
	}
}

func containsInOrder(s string, subs ...string) bool {
	idx := 0
	for _, sub := range subs {
		pos := findFrom(s, sub, idx)
		if pos < 0 {
			return false
		}
		idx = pos + len(sub)
	}
	return true
}

func findFrom(s, sub string, from int) int {
	if from > len(s) {
		return -1
	}
	pos := 0
	for i := from; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return pos + i - from
		}
	}
	return -1
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
go test ./pkg/memory/... -run TestStore -v
```

Expected: FAIL with undefined `Store` / `NewStore`。

- [ ] **Step 3: 实现 `pkg/memory/store.go`**

```go
package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Target identifies a memory channel.
type Target int

const (
	MemoryTarget Target = iota
	UserTarget
)

func targetName(t Target) string {
	switch t {
	case UserTarget:
		return "USER"
	default:
		return "MEMORY"
	}
}

// Limits holds per-channel character caps. Zero values fall back to defaults.
type Limits struct {
	MemoryCharLimit int
	UserCharLimit   int
}

// Store reads and writes memory files. It combines global (user home) and
// project (cwd) files for reads, and writes to the global file only.
type Store struct {
	globalDir  string
	projectDir string
	limits     Limits
}

// NewStore creates a store rooted at cwd. The global directory is
// ~/.lcoder/memory.
func NewStore(cwd string) (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("home dir: %w", err)
	}
	return &Store{
		globalDir:  filepath.Join(home, ".lcoder", "memory"),
		projectDir: filepath.Join(cwd, ".lcoder", "memory"),
		limits: Limits{
			MemoryCharLimit: DefaultMemoryCharLimit,
			UserCharLimit:   DefaultUserCharLimit,
		},
	}, nil
}

// WithLimits overrides the default character limits.
func (s *Store) WithLimits(l Limits) *Store {
	s.limits = l
	return s
}

func (s *Store) limitFor(t Target) int {
	switch t {
	case UserTarget:
		if s.limits.UserCharLimit > 0 {
			return s.limits.UserCharLimit
		}
		return DefaultUserCharLimit
	default:
		if s.limits.MemoryCharLimit > 0 {
			return s.limits.MemoryCharLimit
		}
		return DefaultMemoryCharLimit
	}
}

func (s *Store) globalPath(t Target) string {
	name := targetName(t) + ".md"
	return filepath.Join(s.globalDir, name)
}

func (s *Store) projectPath(t Target) string {
	name := targetName(t) + ".md"
	return filepath.Join(s.projectDir, name)
}

func (s *Store) loadFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return parseEntries(string(data)), nil
}

func (s *Store) saveFile(path string, t Target, entries []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return err
	}
	data := formatFile(targetName(t), entries, s.limitFor(t))
	return os.WriteFile(path, []byte(data), 0640)
}

// GlobalEntries returns entries from the global file.
func (s *Store) GlobalEntries(t Target) ([]string, error) {
	return s.loadFile(s.globalPath(t))
}

// ProjectEntries returns entries from the project file.
func (s *Store) ProjectEntries(t Target) ([]string, error) {
	return s.loadFile(s.projectPath(t))
}

func (s *Store) allEntries(t Target) ([]string, error) {
	global, err := s.GlobalEntries(t)
	if err != nil {
		return nil, err
	}
	project, err := s.ProjectEntries(t)
	if err != nil {
		return nil, err
	}
	return append(global, project...), nil
}

func (s *Store) textFor(t Target) (string, error) {
	entries, err := s.allEntries(t)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", nil
	}
	title := "Agent memory"
	if t == UserTarget {
		title = "User profile"
	}
	return title + ":\n\n" + strings.Join(entries, "\n\n"), nil
}

// MemoryText returns the merged global+project memory text for injection.
func (s *Store) MemoryText() (string, error) { return s.textFor(MemoryTarget) }

// UserText returns the merged global+project user profile text for injection.
func (s *Store) UserText() (string, error) { return s.textFor(UserTarget) }

// Add appends a new entry to the global file. Duplicate entries are silently ignored.
func (s *Store) Add(t Target, content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return fmt.Errorf("content cannot be empty")
	}
	entries, err := s.GlobalEntries(t)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e == content {
			return nil
		}
	}
	limit := s.limitFor(t)
	if charCount(entries)+len(content) > limit {
		return fmt.Errorf("%s at %d/%d chars. Adding this entry (%d chars) would exceed the limit. Consolidate now: use 'replace' to merge overlapping entries into shorter ones or 'remove' stale entries, then retry this add.", targetName(t), charCount(entries), limit, len(content))
	}
	entries = append(entries, content)
	return s.saveFile(s.globalPath(t), t, entries)
}

// Replace updates the unique entry matching oldText with content.
func (s *Store) Replace(t Target, oldText, content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return fmt.Errorf("content cannot be empty")
	}
	entries, err := s.GlobalEntries(t)
	if err != nil {
		return err
	}
	idx, err := findEntryIndex(entries, oldText)
	if err != nil {
		return err
	}
	newEntries := make([]string, len(entries))
	copy(newEntries, entries)
	newEntries[idx] = content
	limit := s.limitFor(t)
	if charCount(newEntries) > limit {
		return fmt.Errorf("%s at %d/%d chars. Replacing would exceed the limit. Shorten the new content or remove other entries first.", targetName(t), charCount(entries), limit)
	}
	return s.saveFile(s.globalPath(t), t, newEntries)
}

// Remove deletes the unique entry matching oldText.
func (s *Store) Remove(t Target, oldText string) error {
	entries, err := s.GlobalEntries(t)
	if err != nil {
		return err
	}
	idx, err := findEntryIndex(entries, oldText)
	if err != nil {
		return err
	}
	entries = append(entries[:idx], entries[idx+1:]...)
	return s.saveFile(s.globalPath(t), t, entries)
}

// UsageString returns "used/limit" for the global channel.
func (s *Store) UsageString(t Target) (string, error) {
	entries, err := s.GlobalEntries(t)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d/%d", charCount(entries), s.limitFor(t)), nil
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
go test ./pkg/memory/... -v
```

Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add pkg/memory/store.go pkg/memory/store_test.go
git commit -m "feat(memory): implement global+project memory store with limits"
```

---

## Task 5: 扩展 `contextmgr` 支持新 BlockKind

**Files:**
- Modify: `pkg/contextmgr/block.go`
- Test: `pkg/contextmgr/block_test.go`

- [ ] **Step 1: 编写失败测试**

```go
package contextmgr

import "testing"

func TestMemoryBlocksAreSystemBlocks(t *testing.T) {
	for _, kind := range []BlockKind{BlockMemory, BlockUserProfile} {
		b := NewBlock(kind, string(kind), StabilityStable, 70)
		if !IsSystemBlock(b) {
			t.Fatalf("kind %q should be a system block", kind)
		}
	}
}

func TestDefaultBlockOrderIncludesMemory(t *testing.T) {
	order := DefaultBlockOrder()
	expected := []BlockKind{BlockSystem, BlockMode, BlockSkills, BlockProjectDocs, BlockMemory, BlockUserProfile, BlockSummary, BlockRetrieval, BlockRecent}
	if len(order) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, order)
	}
	for i, k := range expected {
		if order[i] != k {
			t.Fatalf("position %d: expected %q, got %q", i, k, order[i])
		}
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
go test ./pkg/contextmgr/... -run 'TestMemoryBlocksAreSystemBlocks|TestDefaultBlockOrderIncludesMemory' -v
```

Expected: FAIL with undefined constants.

- [ ] **Step 3: 修改 `pkg/contextmgr/block.go`**

新增常量：

```go
const (
	BlockSystem      BlockKind = "system"
	BlockMode        BlockKind = "mode"
	BlockSkills      BlockKind = "skills"
	BlockProjectDocs BlockKind = "project_docs"
	BlockToolDefs    BlockKind = "tool_defs"
	BlockSummary     BlockKind = "summary"
	BlockRecent      BlockKind = "recent"
	BlockRetrieval   BlockKind = "retrieval"
	BlockMemory      BlockKind = "memory"       // agent personal notes
	BlockUserProfile BlockKind = "user_profile" // user profile
)
```

更新 `IsSystemBlock`：

```go
func IsSystemBlock(b *Block) bool {
	switch b.Kind {
	case BlockSystem, BlockMode, BlockSkills, BlockProjectDocs, BlockMemory, BlockUserProfile:
		return true
	}
	return false
}
```

更新 `DefaultBlockOrder`：

```go
func DefaultBlockOrder() []BlockKind {
	return []BlockKind{
		BlockSystem,
		BlockMode,
		BlockSkills,
		BlockProjectDocs,
		BlockMemory,
		BlockUserProfile,
		BlockSummary,
		BlockRetrieval,
		BlockRecent,
	}
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
go test ./pkg/contextmgr/... -run 'TestMemoryBlocksAreSystemBlocks|TestDefaultBlockOrderIncludesMemory' -v
```

Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add pkg/contextmgr/block.go pkg/contextmgr/block_test.go
git commit -m "feat(contextmgr): add memory and user_profile system blocks"
```

---

## Task 6: 创建 `memory` 内置工具

**Files:**
- Create: `pkg/tools/builtin/memory.go`
- Test: `pkg/tools/builtin/memory_test.go`

- [ ] **Step 1: 编写失败测试**

```go
package builtin

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lcoder/lcoder/pkg/memory"
)

func TestMemoryToolAddAndList(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(filepath.Join(home, ".lcoder", "memory"), 0750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	store, err := memory.NewStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	tool := NewMemory(repo, store)

	res, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"action":  "add",
		"target":  "memory",
		"content": "Prefer parallel tool calls.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text() == "" {
		t.Fatal("expected non-empty result")
	}

	entries, err := store.GlobalEntries(memory.MemoryTarget)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0] != "Prefer parallel tool calls." {
		t.Fatalf("unexpected entries: %v", entries)
	}
}

func TestMemoryToolRejectsMissingOldText(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	t.Setenv("HOME", home)
	store, err := memory.NewStore(tmp)
	if err != nil {
		t.Fatal(err)
	}
	tool := NewMemory(tmp, store)
	_, err = tool.Execute(context.Background(), "call-2", map[string]any{
		"action": "remove",
		"target": "memory",
	})
	if err == nil {
		t.Fatal("expected error for missing old_text")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
go test ./pkg/tools/builtin/... -run TestMemoryTool -v
```

Expected: FAIL with undefined `NewMemory`。

- [ ] **Step 3: 实现 `pkg/tools/builtin/memory.go`**

```go
package builtin

import (
	"context"
	"fmt"

	"github.com/lcoder/lcoder/pkg/memory"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/tools"
)

// Memory manages persistent global memory entries.
type Memory struct {
	cwd   string
	store *memory.Store
}

// NewMemory creates the memory tool bound to cwd and the memory store.
func NewMemory(cwd string, store *memory.Store) tools.Executable {
	return &Memory{cwd: cwd, store: store}
}

func (m *Memory) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "memory",
		Description: "Manage persistent global memory across sessions. " +
			"Use this to save user preferences, project conventions, environment facts, or lessons learned. " +
			"The tool operates on the global memory file; project-level memory files are read-only and can be edited manually.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []any{"add", "replace", "remove"},
					"description": "Operation to perform.",
				},
				"target": map[string]any{
					"type":        "string",
					"enum":        []any{"memory", "user"},
					"description": "Which memory channel to modify.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "New entry text for add/replace.",
				},
				"old_text": map[string]any{
					"type":        "string",
					"description": "Short unique substring of the entry to replace or remove.",
				},
			},
			"required": []any{"action", "target"},
		},
		ExecutionMode: models.ExecutionSequential,
	}
}

func (m *Memory) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	action, _ := args["action"].(string)
	targetName, _ := args["target"].(string)
	content, _ := args["content"].(string)
	oldText, _ := args["old_text"].(string)

	var target memory.Target
	switch targetName {
	case "user":
		target = memory.UserTarget
	case "memory":
		target = memory.MemoryTarget
	default:
		return models.ToolExecutionResult{}, fmt.Errorf("target must be 'memory' or 'user'")
	}

	var err error
	switch action {
	case "add":
		if content == "" {
			return models.ToolExecutionResult{}, fmt.Errorf("content is required for add")
		}
		err = m.store.Add(target, content)
	case "replace":
		if oldText == "" {
			return models.ToolExecutionResult{}, fmt.Errorf("old_text is required for replace")
		}
		if content == "" {
			return models.ToolExecutionResult{}, fmt.Errorf("content is required for replace")
		}
		err = m.store.Replace(target, oldText, content)
	case "remove":
		if oldText == "" {
			return models.ToolExecutionResult{}, fmt.Errorf("old_text is required for remove")
		}
		err = m.store.Remove(target, oldText)
	default:
		return models.ToolExecutionResult{}, fmt.Errorf("action must be 'add', 'replace' or 'remove'")
	}
	if err != nil {
		return models.ToolExecutionResult{}, err
	}

	usage, _ := m.store.UsageString(target)
	msg := fmt.Sprintf("Memory updated (%s). Usage: %s.", targetName, usage)
	return models.ToolExecutionResult{
		Content: []models.ContentPart{models.TextContent{Text: msg}},
		Details: map[string]any{"usage": usage, "target": targetName, "action": action},
	}, nil
}

var _ tools.Executable = (*Memory)(nil)
```

- [ ] **Step 4: 运行测试确认通过**

```bash
go test ./pkg/tools/builtin/... -run TestMemoryTool -v
```

Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add pkg/tools/builtin/memory.go pkg/tools/builtin/memory_test.go
git commit -m "feat(tools): add memory built-in tool"
```

---

## Task 7: 在 `agentsetup` 注入记忆块

**Files:**
- Modify: `pkg/agentsetup/setup.go`
- Modify: `pkg/agentsetup/setup_test.go`
- Modify: `test/integration/agent_realrun_test.go`
- Modify: `test/integration/parallel_tools_test.go`
- Modify: `cmd/lcoder/main.go`

- [ ] **Step 1: 修改 `NewContextManager` 签名并注入 memory blocks**

在 `pkg/agentsetup/setup.go` 中：

```go
import (
	"os"
	"path/filepath"

	"github.com/lcoder/lcoder/pkg/compaction"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/memory"
	"github.com/lcoder/lcoder/pkg/models"
)
```

将函数签名改为：

```go
func NewContextManager(cfg config.Config, budget config.TokenBudget, llmClient *llm.Client, contextText, skillsBlock string, activeMessages []models.AgentMessage, memStore *memory.Store) *contextmgr.Manager {
```

在 skills block 注入之后、recent block 注入之前，添加：

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

- [ ] **Step 2: 更新 `pkg/agentsetup/setup_test.go` 中的调用**

将现有两处 `NewContextManager(...)` 末尾的 `nil` 改为 `nil`（已经是 nil），但签名增加参数后只需保持最后一个参数为 `nil` 即可。若测试原本有 6 个参数，现改为 7 个：

```go
mgr := NewContextManager(cfg, config.TokenBudget{MaxTotal: 100000, TargetTotal: 90000, ReserveOutput: 8192}, nil, "project context here", "skill block here", nil, nil)
```

- [ ] **Step 3: 新增 `TestContextManagerMemoryBlocks`**

在 `pkg/agentsetup/setup_test.go` 末尾添加：

```go
func TestContextManagerMemoryBlocks(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(filepath.Join(home, ".lcoder", "memory"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".lcoder", "memory"), 0750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	if err := os.WriteFile(filepath.Join(home, ".lcoder", "memory", "USER.md"), []byte("User prefers Chinese."), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".lcoder", "memory", "MEMORY.md"), []byte("Project uses Go modules."), 0640); err != nil {
		t.Fatal(err)
	}

	store, err := memory.NewStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Context: config.ContextConfig{MinRecent: 1}, Memory: config.MemoryConfig{Enabled: true}}
	mgr := NewContextManager(cfg, config.TokenBudget{MaxTotal: 100000, TargetTotal: 90000, ReserveOutput: 8192}, nil, "", "", nil, store)

	if _, ok := mgr.GetBlock(contextmgr.BlockUserProfile, "user_profile"); !ok {
		t.Fatal("missing user_profile block")
	}
	if _, ok := mgr.GetBlock(contextmgr.BlockMemory, "memory"); !ok {
		t.Fatal("missing memory block")
	}

	merged := mgr.SystemPrompt()
	if !strings.Contains(merged, "User prefers Chinese") || !strings.Contains(merged, "Go modules") {
		t.Fatalf("system prompt should include memory text, got:\n%s", merged)
	}
}
```

需要在该测试文件顶部增加 `os`、`path/filepath`、`strings` 以及 `github.com/lcoder/lcoder/pkg/memory` 的 import。

- [ ] **Step 4: 更新集成测试调用**

在 `test/integration/agent_realrun_test.go:364` 和 `test/integration/parallel_tools_test.go:246`，将 `agentsetup.NewContextManager(...)` 末尾增加一个 `nil` 参数：

```go
mgr := agentsetup.NewContextManager(cfg, cfgBudget, client, contextText, skillsBlock, nil, nil)
// parallel_tools:
mgr := agentsetup.NewContextManager(cfg, cfgBudget, client, "", "", nil, nil)
```

- [ ] **Step 5: 运行相关测试**

```bash
go test ./pkg/agentsetup/... ./pkg/contextmgr/... -v
```

Expected: PASS。

- [ ] **Step 6: Commit**

```bash
git add pkg/agentsetup/setup.go pkg/agentsetup/setup_test.go test/integration/agent_realrun_test.go test/integration/parallel_tools_test.go
git commit -m "feat(agentsetup): inject memory and user_profile blocks when enabled"
```

---

## Task 8: 在 CLI 中构造 Store 并注册 `memory` 工具

**Files:**
- Modify: `cmd/lcoder/main.go`

- [ ] **Step 1: 调整 import**

将 `_ "github.com/lcoder/lcoder/pkg/tools/builtin"` 改为命名导入：

```go
builtinTools "github.com/lcoder/lcoder/pkg/tools/builtin"
```

新增：

```go
"github.com/lcoder/lcoder/pkg/memory"
```

- [ ] **Step 2: 在 `prepareAgent` 中创建 store 并注册工具**

在 `registry := tools.NewRegistry(cwd)` 之前添加：

```go
var memStore *memory.Store
if cfg.Memory.Enabled {
	var err error
	memStore, err = memory.NewStore(cwd)
	if err != nil {
		return nil, fmt.Errorf("init memory store: %w", err)
	}
	memStore.WithLimits(memory.Limits{
		MemoryCharLimit: cfg.Memory.MemoryCharLimit,
		UserCharLimit:   cfg.Memory.UserCharLimit,
	})
}
```

将 `mgr := agentsetup.NewContextManager(...)` 改为传入 `memStore`：

```go
mgr := agentsetup.NewContextManager(cfg, budget, llmClient, contextText, skillsBlock, sess.ActiveMessages(), memStore)
```

在 `registry.RegisterBuiltinFactories(cwd)` 之后添加：

```go
if memStore != nil {
	registry.Register("memory", builtinTools.NewMemory(cwd, memStore))
}
```

- [ ] **Step 3: 编译检查**

```bash
go build ./cmd/lcoder
```

Expected: 无错误。

- [ ] **Step 4: Commit**

```bash
git add cmd/lcoder/main.go
git commit -m "feat(cli): wire memory store and register memory tool"
```

---

## Task 9: 更新示例配置

**Files:**
- Modify: `configs/lcoder.yaml`

- [ ] **Step 1: 在 `context:` 块后添加注释化的 `memory:` 配置**

```yaml
# Persistent memory. Lcoder loads ~/.lcoder/memory/{MEMORY,USER}.md and
# <repo>/.lcoder/memory/{MEMORY,USER}.md into the system prompt.
# The memory tool writes to the global files only.
memory:
  enabled: true
  # memory_char_limit: 2200
  # user_char_limit: 1375
```

- [ ] **Step 2: Commit**

```bash
git add configs/lcoder.yaml
git commit -m "docs(config): add memory section to sample config"
```

---

## Task 10: 全量验证

**Files:** 全部已修改文件。

- [ ] **Step 1: 运行 memory / contextmgr / agentsetup / tools 单元测试**

```bash
go test ./pkg/memory/... ./pkg/contextmgr/... ./pkg/agentsetup/... ./pkg/tools/builtin/... -v
```

Expected: 全部 PASS。

- [ ] **Step 2: 运行全仓库单元测试（排除 reference/Shannon）**

```bash
go test $(go list ./... | grep -v 'reference/Shannon')
```

Expected: 全部 PASS。

- [ ] **Step 3: 编译全部二进制**

```bash
go build ./...
```

Expected: 无错误。

- [ ] **Step 4: 运行集成测试（如本地有 API key）**

```bash
go test ./test/integration/... -tags integration
```

Expected: PASS（或跳过需要网络的部分）。

- [ ] **Step 5: 手动验证记忆注入**

在任意仓库目录执行：

```bash
mkdir -p ~/.lcoder/memory
printf "User prefers concise replies." > ~/.lcoder/memory/USER.md
go run ./cmd/lcoder "调用 memory 工具添加一条记忆: target=memory action=add content='Always run go vet before committing'" --json -p ""
```

注意：实际手动验证时通过 JSON 输出观察 `ToolExecutionEndEvent` 中 `memory` 工具返回成功，并检查下一轮系统提示词包含新增记忆。

- [ ] **Step 6: Commit 任何测试修复**

```bash
git add .
git commit -m "test(memory): add full test suite and verify build"
```

---

## Self-Review Checklist

| 规范点 | 对应任务 |
|---|---|
| 全局/项目 `MEMORY.md` / `USER.md` 四层文件 | Task 4 |
| `memory`/`user_profile` 作为 stable system block 注入 | Task 5, 7 |
| `DefaultBlockOrder` 顺序为 system → mode → skills → project_docs → memory → user_profile → summary → retrieval → recent | Task 5 |
| `memory` 工具支持 add/replace/remove | Task 6 |
| 字符上限（memory 2200 / user 1375）与超限错误 | Task 2, 3, 4 |
| 配置 `memory.enabled` 与可选 limit 覆盖 | Task 1 |
| CLI 在启用时注册 `memory` 工具、禁用时不注册 | Task 8 |
| 不修改 `reference/` | 无 |
| 所有 `NewContextManager` 调用同步更新 | Task 7 |
| 无占位符/TBD | 全文 |

---

## 执行方式

**Plan complete and saved to `docs/superpowers/plans/2026-07-06-memory-system.md`. Two execution options:**

1. **Subagent-Driven (recommended)** - dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** - execute tasks in this session using executing-plans, batch execution with checkpoints.

**Which approach?**
