# 工具并发调度(ToolAccess + 批次调度器 + 两阶段 executor)实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用资源访问声明 + 批次调度器替代 `ExecutionMode` 静态标签,实现细粒度工具并发,并完全退役 `execution_mode` 相关的一切用法。

**Architecture:** 工具通过可选接口 `tools.AccessDeclarer` 声明每次调用访问的资源(文件路径 + 读/写/搜索/all);`executor` 拆为串行 prepare(校验+路径守卫+权限确认+hook)与并发 run(纯执行)两阶段;`batchScheduler` 按冲突边让调用等待"所有排在前面的冲突调用"。设计语义逐行核对自 Kimi Code `tool-access.ts` / `tool-scheduler.ts`(见 `docs/tool-concurrency-optimization.md`)。

**Tech Stack:** Go 1.25,无新依赖。

**关键背景(实施前必读):**
- 当前 executor 全貌:`pkg/agent/executor.go`(`execute` → `executeSequential`/`executeParallel` → `executeOneToolCall`)。
- 内建工具均有 `cwd` 字段,路径解析用 `resolveInCwd(cwd, raw)`(`pkg/tools/builtin/fspath.go:11`)。
- 批次完全已知,调度器退化为"等待前面所有冲突调用完成",冲突边沿索引链传递,等价于 Kimi 的 active+queued FIFO 语义(论证见设计文档)。
- 项目处于开发阶段,不需要向后兼容(含 checkpoint JSON 字段删除)。

---

### Task 1: `pkg/tools/access.go` — ToolAccess 类型与冲突判定

**Files:**
- Create: `pkg/tools/access.go`
- Test: `pkg/tools/access_test.go`

- [ ] **Step 1: 写失败测试**

```go
package tools

import "testing"

func TestAccessConflict(t *testing.T) {
	read := func(p string) ToolAccess { return ToolAccess{Op: OpRead, Path: p} }
	write := func(p string) ToolAccess { return ToolAccess{Op: OpWrite, Path: p} }
	rw := func(p string) ToolAccess { return ToolAccess{Op: OpReadWrite, Path: p} }
	search := func(p string) ToolAccess { return ToolAccess{Op: OpSearch, Path: p, Recursive: true} }
	all := ToolAccess{Op: OpAll}

	tests := []struct {
		name string
		a, b ToolAccess
		want bool
	}{
		{"read+read same file", read("/w/a.go"), read("/w/a.go"), false},
		{"read+write same file", read("/w/a.go"), write("/w/a.go"), true},
		{"write+write diff files", write("/w/a.go"), write("/w/b.go"), false},
		{"readwrite+read same file", rw("/w/a.go"), read("/w/a.go"), true},
		{"search+read under tree", search("/w"), read("/w/sub/a.go"), false},
		{"search+write under tree", search("/w"), write("/w/sub/a.go"), true},
		{"write tree + read under", ToolAccess{Op: OpWrite, Path: "/w", Recursive: true}, read("/w/a.go"), true},
		{"case-insensitive same file", read("/w/A.go"), write("/w/a.go"), true},
		{"backslash vs slash", read(`C:\w\a.go`), write("C:/w/a.go"), true},
		{"trailing slash dir", search("/w/"), write("/w/a.go"), true},
		{"all vs read", all, read("/w/a.go"), true},
		{"all vs all", all, all, true},
		{"non-recursive dir vs child", ToolAccess{Op: OpWrite, Path: "/w"}, read("/w/a.go"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AccessConflict(tt.a, tt.b); got != tt.want {
				t.Fatalf("AccessConflict(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestAccessesConflictSets(t *testing.T) {
	left := []ToolAccess{{Op: OpRead, Path: "/w/a.go"}}
	right := []ToolAccess{{Op: OpWrite, Path: "/w/b.go"}, {Op: OpWrite, Path: "/w/a.go"}}
	if !AccessesConflict(left, right) {
		t.Fatal("expected conflict via second access in set")
	}
	if AccessesConflict(left, []ToolAccess{{Op: OpRead, Path: "/w/a.go"}}) {
		t.Fatal("read+read must not conflict")
	}
	if AccessesConflict(nil, []ToolAccess{{Op: OpAll}}) {
		t.Fatal("empty set conflicts with nothing")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/tools -run 'TestAccessConflict|TestAccessesConflictSets' -v`
Expected: FAIL(编译错误:`undefined: ToolAccess` 等)

- [ ] **Step 3: 实现 `pkg/tools/access.go`**

```go
package tools

import "strings"

// AccessOperation describes how a tool call touches a resource.
// Mirrors Kimi Code's ToolFileAccessOperation plus the globally-exclusive
// 'all' variant (tool-access.ts).
type AccessOperation string

const (
	OpRead      AccessOperation = "read"
	OpWrite     AccessOperation = "write"
	OpReadWrite AccessOperation = "readwrite"
	OpSearch    AccessOperation = "search"
	// OpAll marks arbitrary side effects that cannot be expressed as a file
	// access; it conflicts with everything, including itself.
	OpAll AccessOperation = "all"
)

// ToolAccess declares one resource a tool call will touch. The agent's batch
// scheduler uses these declarations to decide which calls may overlap.
type ToolAccess struct {
	Op        AccessOperation
	Path      string // resolved absolute path; empty when Op == OpAll
	Recursive bool   // Path is a directory tree root
}

// AccessDeclarer is an optional interface a tool implements to declare the
// resources a call will touch, enabling fine-grained concurrent scheduling.
// Tools that do not implement it are treated as OpAll (fully serial).
type AccessDeclarer interface {
	DeclareAccesses(args map[string]any) []ToolAccess
}

// AccessesConflict reports whether any access in left conflicts with any in
// right.
func AccessesConflict(left, right []ToolAccess) bool {
	for _, l := range left {
		for _, r := range right {
			if AccessConflict(l, r) {
				return true
			}
		}
	}
	return false
}

// AccessConflict reports whether two accesses conflict: at least one writes
// (or is OpAll) and their paths overlap.
func AccessConflict(a, b ToolAccess) bool {
	if a.Op == OpAll || b.Op == OpAll {
		return true
	}
	if !accessWrites(a.Op) && !accessWrites(b.Op) {
		return false
	}
	return accessPathsOverlap(a, b)
}

func accessWrites(op AccessOperation) bool {
	return op == OpWrite || op == OpReadWrite
}

func accessPathsOverlap(a, b ToolAccess) bool {
	ap := normalizeAccessPath(a.Path)
	bp := normalizeAccessPath(b.Path)
	if ap == bp {
		return true
	}
	return (a.Recursive && strings.HasPrefix(bp, ap+"/")) ||
		(b.Recursive && strings.HasPrefix(ap, bp+"/"))
}

// normalizeAccessPath canonicalizes a path for conflict comparison:
// backslashes to slashes, duplicate slashes collapsed, lowercased (Windows
// and default macOS filesystems are case-insensitive), trailing slash
// stripped. Mirrors Kimi Code's normalizePath in tool-access.ts.
func normalizeAccessPath(path string) string {
	p := strings.ReplaceAll(path, "\\", "/")
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	p = strings.ToLower(p)
	if len(p) > 1 {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./pkg/tools -run 'TestAccessConflict|TestAccessesConflictSets' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/tools/access.go pkg/tools/access_test.go
git commit -m "feat(tools): add ToolAccess declarations and conflict rules

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: 内建工具实现 `AccessDeclarer`

**Files:**
- Modify: `pkg/tools/builtin/read.go`、`write.go`、`edit.go`、`grep.go`、`find.go`、`ls.go`、`bash.go`(各加一个方法)
- Test: `pkg/tools/builtin/access_test.go`(新建)

设计要点:
- 路径一律经 `resolveInCwd(t.cwd, raw)` 解析为绝对路径,保证冲突比较口径一致。
- 声明失败(缺 path)保守返回 `OpAll`;正常路径下 `ValidateArgs` 已在 executor prepare 阶段拦过,这只是防御。
- `ls` 声明 `Recursive: true`:目录下直接子项的增删会改变列表结果,递归声明才能覆盖;更深层的误伤可接受。
- `todo`/`skill`/`subagent`/HTTP/MCP 工具**不实现**该接口 → 默认 `OpAll`,行为不劣于现状。
- `edit` 是 `OpReadWrite`(要先读再写),`write` 是纯 `OpWrite`——与 Kimi 一致。

- [ ] **Step 1: 写失败测试**

```go
package builtin

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lcoder/lcoder/pkg/tools"
)

func TestBuiltinAccessDeclarations(t *testing.T) {
	cwd := t.TempDir()
	tests := []struct {
		name string
		tool tools.Executable
		args map[string]any
		want []tools.ToolAccess
	}{
		{"read", NewRead(cwd), map[string]any{"path": "a.go"},
			[]tools.ToolAccess{{Op: tools.OpRead, Path: resolveInCwd(cwd, "a.go")}}},
		{"write", NewWrite(cwd), map[string]any{"path": "a.go"},
			[]tools.ToolAccess{{Op: tools.OpWrite, Path: resolveInCwd(cwd, "a.go")}}},
		{"edit", NewEdit(cwd), map[string]any{"path": "a.go"},
			[]tools.ToolAccess{{Op: tools.OpReadWrite, Path: resolveInCwd(cwd, "a.go")}}},
		{"grep default root", NewGrep(cwd), map[string]any{"pattern": "x"},
			[]tools.ToolAccess{{Op: tools.OpSearch, Path: resolveInCwd(cwd, cwd), Recursive: true}}},
		{"grep explicit dir", NewGrep(cwd), map[string]any{"pattern": "x", "path": "sub"},
			[]tools.ToolAccess{{Op: tools.OpSearch, Path: resolveInCwd(cwd, "sub"), Recursive: true}}},
		{"find", NewFind(cwd), map[string]any{"pattern": "*.go"},
			[]tools.ToolAccess{{Op: tools.OpSearch, Path: resolveInCwd(cwd, cwd), Recursive: true}}},
		{"ls", NewLs(cwd), map[string]any{},
			[]tools.ToolAccess{{Op: tools.OpRead, Path: resolveInCwd(cwd, cwd), Recursive: true}}},
		{"bash", NewBash(cwd), map[string]any{"command": "go build ./..."},
			[]tools.ToolAccess{{Op: tools.OpAll}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			declarer, ok := tt.tool.(tools.AccessDeclarer)
			if !ok {
				t.Fatalf("%T does not implement tools.AccessDeclarer", tt.tool)
			}
			if got := declarer.DeclareAccesses(tt.args); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("DeclareAccesses(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
	_ = filepath.Separator // keep filepath import if needed by adjustments
}

// 未实现接口的工具(以 TodoWrite 为代表)在 executor 侧按 OpAll 处理。
func TestToolWithoutDeclarationIsNotDeclarer(t *testing.T) {
	if _, ok := NewTodoWrite(t.TempDir()).(tools.AccessDeclarer); ok {
		t.Fatal("TodoWrite must NOT implement AccessDeclarer; default is OpAll")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/tools/builtin -run 'TestBuiltinAccessDeclarations|TestToolWithoutDeclarationIsNotDeclarer' -v`
Expected: FAIL(类型断言不成立 / 编译错误)

- [ ] **Step 3: 为 7 个工具各加 `DeclareAccesses`**

`read.go`(追加):

```go
// DeclareAccesses implements tools.AccessDeclarer: a read only touches its file.
func (r *Read) DeclareAccesses(args map[string]any) []tools.ToolAccess {
	path, err := tools.RequiredString(args, "path")
	if err != nil {
		return []tools.ToolAccess{{Op: tools.OpAll}}
	}
	return []tools.ToolAccess{{Op: tools.OpRead, Path: resolveInCwd(r.cwd, path)}}
}
```

`write.go`:同构,`Op` 改为 `tools.OpWrite`,接收者 `*Write`。

`edit.go`:同构,`Op` 改为 `tools.OpReadWrite`,接收者 `*Edit`。

`grep.go`:

```go
// DeclareAccesses implements tools.AccessDeclarer: grep searches a tree read-only.
func (g *Grep) DeclareAccesses(args map[string]any) []tools.ToolAccess {
	root := g.cwd
	if v := tools.String(args, "path", ""); v != "" {
		root = v
	}
	return []tools.ToolAccess{{Op: tools.OpSearch, Path: resolveInCwd(g.cwd, root), Recursive: true}}
}
```

`find.go`:同 grep(`find.go` 的 path 读取方式是 `args["path"].(string)`,与 `ls.go` 相同,任选一种风格保持一致),接收者 `*Find`。

`ls.go`:

```go
// DeclareAccesses implements tools.AccessDeclarer: listing a directory is
// affected by writes anywhere below it, so the read is declared recursive.
func (l *Ls) DeclareAccesses(args map[string]any) []tools.ToolAccess {
	path := l.cwd
	if v, ok := args["path"].(string); ok && v != "" {
		path = v
	}
	return []tools.ToolAccess{{Op: tools.OpRead, Path: resolveInCwd(l.cwd, path), Recursive: true}}
}
```

`bash.go`:

```go
// DeclareAccesses implements tools.AccessDeclarer: a shell command may have
// arbitrary side effects, so it conflicts with everything.
func (b *Bash) DeclareAccesses(args map[string]any) []tools.ToolAccess {
	return []tools.ToolAccess{{Op: tools.OpAll}}
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./pkg/tools/builtin -run 'TestBuiltinAccessDeclarations|TestToolWithoutDeclarationIsNotDeclarer' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/tools/builtin/
git commit -m "feat(tools/builtin): declare resource accesses for file tools and bash

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: `pkg/agent/scheduler.go` — 批次调度器

**Files:**
- Create: `pkg/agent/scheduler.go`
- Test: `pkg/agent/scheduler_test.go`

- [ ] **Step 1: 写失败测试**

```go
package agent

import (
	"context"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/tools"
)

func writeAccess(p string) []tools.ToolAccess {
	return []tools.ToolAccess{{Op: tools.OpWrite, Path: p}}
}

func TestSchedulerIndependentCallDoesNotWait(t *testing.T) {
	s := newBatchScheduler([][]tools.ToolAccess{writeAccess("/w/a.go"), writeAccess("/w/b.go")})
	done := make(chan error, 1)
	go func() { done <- s.wait(context.Background(), 1) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wait: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("call 1 must not wait for non-conflicting call 0")
	}
}

func TestSchedulerConflictingCallWaits(t *testing.T) {
	s := newBatchScheduler([][]tools.ToolAccess{writeAccess("/w/a.go"), writeAccess("/w/a.go")})
	done := make(chan error, 1)
	go func() { done <- s.wait(context.Background(), 1) }()
	select {
	case <-done:
		t.Fatal("conflicting call returned before finish(0)")
	case <-time.After(100 * time.Millisecond):
	}
	s.finish(0)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wait: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("call 1 still blocked after finish(0)")
	}
}

// FIFO: [read x, write x, read x] —— call 2 与 call 0 不冲突,但与排在
// 前面的 call 1 冲突,因此必须等 call 1(不允许插队)。
func TestSchedulerFIFOChain(t *testing.T) {
	read := []tools.ToolAccess{{Op: tools.OpRead, Path: "/w/x"}}
	write := writeAccess("/w/x")
	s := newBatchScheduler([][]tools.ToolAccess{read, write, read})
	s.finish(0)
	done := make(chan error, 1)
	go func() { done <- s.wait(context.Background(), 2) }()
	select {
	case <-done:
		t.Fatal("call 2 must wait for earlier conflicting call 1 even after finish(0)")
	case <-time.After(100 * time.Millisecond):
	}
	s.finish(1)
	if err := <-done; err != nil {
		t.Fatalf("wait: %v", err)
	}
}

func TestSchedulerWaitContextCancel(t *testing.T) {
	s := newBatchScheduler([][]tools.ToolAccess{writeAccess("/w/a"), writeAccess("/w/a")})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.wait(ctx, 1); err == nil {
		t.Fatal("expected context error for canceled wait")
	}
}

func TestSchedulerAddWait(t *testing.T) {
	// 两个 read 不冲突,但 addWait 显式加边后 call 1 必须等 call 0。
	read := []tools.ToolAccess{{Op: tools.OpRead, Path: "/w/a"}}
	s := newBatchScheduler([][]tools.ToolAccess{read, read})
	s.addWait(1, 0)
	done := make(chan error, 1)
	go func() { done <- s.wait(context.Background(), 1) }()
	select {
	case <-done:
		t.Fatal("addWait edge not honored")
	case <-time.After(100 * time.Millisecond):
	}
	s.finish(0)
	if err := <-done; err != nil {
		t.Fatalf("wait: %v", err)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/agent -run TestScheduler -v`
Expected: FAIL(编译错误:`undefined: newBatchScheduler`)

- [ ] **Step 3: 实现 `pkg/agent/scheduler.go`**

```go
package agent

import (
	"context"

	"github.com/lcoder/lcoder/pkg/tools"
)

// batchScheduler orders one batch of tool calls by resource conflicts.
// Call i waits for every earlier call whose accesses conflict with its own;
// because conflicts chain through earlier indices, this reproduces the
// active+queued FIFO semantics of Kimi Code's ToolScheduler for a batch
// that is fully known upfront. One instance serves exactly one batch.
type batchScheduler struct {
	done  []chan struct{}
	waits [][]int // waits[i]: earlier indices call i must await
}

func newBatchScheduler(accesses [][]tools.ToolAccess) *batchScheduler {
	n := len(accesses)
	s := &batchScheduler{done: make([]chan struct{}, n), waits: make([][]int, n)}
	for i := 0; i < n; i++ {
		s.done[i] = make(chan struct{})
		for j := 0; j < i; j++ {
			if tools.AccessesConflict(accesses[i], accesses[j]) {
				s.waits[i] = append(s.waits[i], j)
			}
		}
	}
	return s
}

// addWait adds a non-resource ordering edge: call i must also await call j.
// Used for same-batch dedup of cacheable calls, where the duplicate must
// observe the original's cached result.
func (s *batchScheduler) addWait(i, j int) {
	s.waits[i] = append(s.waits[i], j)
}

// wait blocks until all calls that i depends on have finished, or ctx is done.
func (s *batchScheduler) wait(ctx context.Context, i int) error {
	for _, j := range s.waits[i] {
		select {
		case <-s.done[j]:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// finish marks call i complete, unblocking later calls that waited on it.
// Each index must be finished exactly once; the executor guarantees this via
// defer, including for short-circuited calls.
func (s *batchScheduler) finish(i int) {
	close(s.done[i])
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./pkg/agent -run TestScheduler -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/agent/scheduler.go pkg/agent/scheduler_test.go
git commit -m "feat(agent): add conflict-based batch scheduler for tool calls

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: executor 两阶段拆分 + 接入调度器

**Files:**
- Modify: `pkg/agent/executor.go`(`execute`/`executeSequential`/`executeParallel`/`executeOneToolCall` 重构为 `execute`/`prepareToolCall`/`runToolCall`/`runSequential`/`runScheduled`/`finalizeBatch`)
- Test: `pkg/agent/executor_scheduler_test.go`(新建)

设计要点(改动理由,实施时保留为代码注释的骨架):
- **prepare 串行**:校验、路径守卫、交互式权限确认、before hook 都按 provider 顺序执行,不能并发交错;只有纯执行并发。被拒/校验失败的调用不发 `ToolExecutionStart`(现状语义保留)。
- **accesses 用最终 args 计算**(hook 改写之后),未实现 `AccessDeclarer` 的工具默认 `OpAll`。
- **dedup 边**:同批次重复的可缓存调用(read/ls/grep/find)通过 `addWait` 等待首个调用,使其 dedup 查找必中——sequential 模式退役后 dedup 依然确定性生效(Kimi v2 的 toolDedupe 等价物)。
- **删除** `Definition().ExecutionMode == sequential` 的整批串行判定:由 `OpAll` 默认以更细粒度覆盖。
- `switch_mode` 整批串行 veto 本任务保留(`execMode` 也是),Task 5 随 `ExecutionMode` 一起移除。

- [ ] **Step 1: 写失败测试**

新建 `pkg/agent/executor_scheduler_test.go`:

```go
package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/tools"
)

// gateTool blocks in Execute until released. It deliberately does NOT
// implement tools.AccessDeclarer; declaringGateTool adds that.
type gateTool struct {
	mu      sync.Mutex
	started []string
	release chan struct{}
}

func newGateTool() *gateTool { return &gateTool{release: make(chan struct{})} }

func (g *gateTool) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "gate",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"path": map[string]any{"type": "string"}},
		},
	}
}

func (g *gateTool) Execute(ctx context.Context, _ string, args map[string]any) (models.ToolExecutionResult, error) {
	p, _ := args["path"].(string)
	g.mu.Lock()
	g.started = append(g.started, p)
	g.mu.Unlock()
	select {
	case <-g.release:
	case <-ctx.Done():
	}
	return models.NewToolExecutionResultText("done"), nil
}

func (g *gateTool) startedCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.started)
}

type declaringGateTool struct {
	*gateTool
	name string
	op   tools.AccessOperation
}

func (d declaringGateTool) Definition() models.ToolDefinition {
	def := d.gateTool.Definition()
	def.Name = d.name
	return def
}

func (d declaringGateTool) DeclareAccesses(args map[string]any) []tools.ToolAccess {
	p, _ := args["path"].(string)
	return []tools.ToolAccess{{Op: d.op, Path: p}}
}

func newSchedulerTestExecutor(reg *tools.Registry) *executor {
	cfg := Config{}
	return &executor{
		cfg:         &cfg,
		mgr:         contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192}),
		registry:    reg,
		permissions: permissions.NewEngine(permissions.DefaultConfig()),
		emitter:     &eventEmitter{bus: events.New()},
		dedup:       make(map[string]models.AgentMessage),
	}
}

func schedCall(id, name, path string) models.ToolCallContent {
	return models.ToolCallContent{Type: "tool_call", ID: id, Name: name, Arguments: map[string]any{"path": path}}
}

func waitForStarts(t *testing.T, g *gateTool, n int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for g.startedCount() < n {
		select {
		case <-deadline:
			t.Fatalf("expected %d started calls, got %v", n, g.started)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestExecutorDifferentFilesOverlap(t *testing.T) {
	gate := newGateTool()
	reg := tools.NewRegistry(".")
	reg.Register("gate", declaringGateTool{gateTool: gate, name: "gate", op: tools.OpReadWrite})
	e := newSchedulerTestExecutor(reg)

	calls := []models.ToolCallContent{schedCall("c1", "gate", "a.go"), schedCall("c2", "gate", "b.go")}
	done := make(chan struct{})
	go func() {
		e.execute(context.Background(), 0, models.AgentMessage{}, calls, models.ExecutionParallel)
		close(done)
	}()

	waitForStarts(t, gate, 2) // 串行调度下第二个永远不会 start
	close(gate.release)
	<-done
}

func TestExecutorSameFileSerializes(t *testing.T) {
	gate := newGateTool()
	reg := tools.NewRegistry(".")
	reg.Register("gate", declaringGateTool{gateTool: gate, name: "gate", op: tools.OpReadWrite})
	e := newSchedulerTestExecutor(reg)

	calls := []models.ToolCallContent{schedCall("c1", "gate", "x.go"), schedCall("c2", "gate", "x.go")}
	done := make(chan []models.AgentMessage, 1)
	go func() {
		results, _ := e.execute(context.Background(), 0, models.AgentMessage{}, calls, models.ExecutionParallel)
		done <- results
	}()

	waitForStarts(t, gate, 1)
	select {
	case <-time.After(100 * time.Millisecond):
	case <-done:
		t.Fatal("batch finished while first call was blocked")
	}
	if n := gate.startedCount(); n != 1 {
		t.Fatalf("conflicting call started early, started = %v", gate.started)
	}
	close(gate.release)
	if results := <-done; len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

// 未实现 AccessDeclarer 的工具默认 OpAll:即便"不同文件"也串行。
func TestExecutorUndeclaredToolDefaultsToAll(t *testing.T) {
	gate := newGateTool()
	reg := tools.NewRegistry(".")
	reg.Register("gate", gate)
	e := newSchedulerTestExecutor(reg)

	calls := []models.ToolCallContent{schedCall("c1", "gate", "a.go"), schedCall("c2", "gate", "b.go")}
	done := make(chan struct{})
	go func() {
		e.execute(context.Background(), 0, models.AgentMessage{}, calls, models.ExecutionParallel)
		close(done)
	}()

	waitForStarts(t, gate, 1)
	select {
	case <-time.After(100 * time.Millisecond):
	case <-done:
		t.Fatal("undeclared tool calls must serialize")
	}
	if n := gate.startedCount(); n != 1 {
		t.Fatalf("undeclared tool ran concurrently, started = %v", gate.started)
	}
	close(gate.release)
	<-done
}

// 交互式权限确认在 prepare 阶段串行发生:两个确认都完成后才有任何执行,
// 且顺序即 provider 顺序。
func TestExecutorConfirmsInProviderOrder(t *testing.T) {
	gate := newGateTool()
	close(gate.release) // 不阻塞执行
	reg := tools.NewRegistry(".")
	reg.Register("gate", declaringGateTool{gateTool: gate, name: "gate", op: tools.OpReadWrite})
	e := newSchedulerTestExecutor(reg)

	var mu sync.Mutex
	var order []string
	e.cfg.UserConfirm = confirmFunc(func(_ context.Context, info ToolCallInfo) (ConfirmResult, error) {
		mu.Lock()
		order = append(order, info.ToolCall.ID)
		mu.Unlock()
		return ConfirmResult{Allow: true, Scope: ScopeOnce}, nil
	})
	e.permissions = permissions.NewEngineFromRules([]permissions.Rule{
		{Tool: "gate", Pattern: "*", Decision: permissions.Ask},
	})

	calls := []models.ToolCallContent{schedCall("c1", "gate", "a.go"), schedCall("c2", "gate", "b.go")}
	e.execute(context.Background(), 0, models.AgentMessage{}, calls, models.ExecutionParallel)

	if len(order) != 2 || order[0] != "c1" || order[1] != "c2" {
		t.Fatalf("confirmations out of provider order: %v", order)
	}
}

// prepare 阶段被拒的调用不占调度位,不阻塞后续调用。
func TestExecutorDeniedCallDoesNotBlock(t *testing.T) {
	gate := newGateTool()
	close(gate.release)
	reg := tools.NewRegistry(".")
	reg.Register("gate", declaringGateTool{gateTool: gate, name: "gate", op: tools.OpReadWrite})
	e := newSchedulerTestExecutor(reg)

	// "../escape.go" 被路径守卫拒绝(prepare 阶段),c2 必须正常执行。
	calls := []models.ToolCallContent{schedCall("c1", "gate", "../escape.go"), schedCall("c2", "gate", "b.go")}
	results, _ := e.execute(context.Background(), 0, models.AgentMessage{}, calls, models.ExecutionParallel)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	first := results[0].Content[0].(models.ToolResultContent)
	if !first.IsError {
		t.Fatal("path-guarded call must be an error result")
	}
	second := results[1].Content[0].(models.ToolResultContent)
	if second.IsError {
		t.Fatalf("later call must not be affected, got %q", second.Text())
	}
	if gate.startedCount() != 1 {
		t.Fatalf("denied call must never execute, started = %v", gate.started)
	}
}

// 同批次重复的可缓存调用通过 addWait 边等待原调用,dedup 必中:
// 工具只执行一次,第二份结果标记 deduplicated。
func TestExecutorDuplicateCacheableCallDeduplicates(t *testing.T) {
	gate := newGateTool()
	reg := tools.NewRegistry(".")
	reg.Register("read", declaringGateTool{gateTool: gate, name: "read", op: tools.OpRead})
	e := newSchedulerTestExecutor(reg)

	calls := []models.ToolCallContent{schedCall("c1", "read", "same.go"), schedCall("c2", "read", "same.go")}
	done := make(chan []models.AgentMessage, 1)
	go func() {
		results, _ := e.execute(context.Background(), 0, models.AgentMessage{}, calls, models.ExecutionParallel)
		done <- results
	}()

	waitForStarts(t, gate, 1)
	close(gate.release)
	results := <-done

	if gate.startedCount() != 1 {
		t.Fatalf("duplicate read must not re-execute, started = %v", gate.started)
	}
	trc := results[1].Content[0].(models.ToolResultContent)
	if trc.Details["deduplicated"] != true {
		t.Fatalf("expected deduplicated detail on second result, got %v", trc.Details)
	}
}

type confirmFunc func(context.Context, ToolCallInfo) (ConfirmResult, error)

func (f confirmFunc) ConfirmWithScope(ctx context.Context, info ToolCallInfo) (ConfirmResult, error) {
	return f(ctx, info)
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/agent -run TestExecutor -v`
Expected: 新并发测试 FAIL(当前实现下同 batch 不同文件也串行/或编译错误若签名已变);已有测试仍 PASS

- [ ] **Step 3: 重构 `pkg/agent/executor.go`**

3a. 在 `executor` 结构之后加类型:

```go
// preparedToolCall is the output of the serial prepare phase. Exactly one of
// resolved/run is set: resolved for calls short-circuited before execution
// (meta tools, validation, path guard, permission, hook block), run for
// calls approved and ready to execute.
type preparedToolCall struct {
	call     models.ToolCallContent
	args     map[string]any
	accesses []tools.ToolAccess
	// alsoWaitFor, when >= 0, adds a non-resource ordering edge: this call is
	// a same-batch duplicate of that runnable index and must await it so its
	// dedup lookup is a guaranteed hit (kimi-code v2's toolDedupe).
	alsoWaitFor int
	resolved    models.AgentMessage
	run         func(ctx context.Context) models.AgentMessage
}
```

3b. `execute` 替换为:

```go
func (e *executor) execute(ctx context.Context, turn int, assistantMsg models.AgentMessage, calls []models.ToolCallContent, execMode models.ExecutionMode) ([]models.AgentMessage, bool) {
	e.dedupMu.Lock()
	e.dedup = make(map[string]models.AgentMessage)
	e.dedupMu.Unlock()
	e.batchLen = len(calls)

	// Phase 1: serial preparation in provider order. Validation, the path
	// guard, interactive permission prompts, and before-hooks all run here so
	// they cannot interleave; only pure execution overlaps in phase 2.
	prepared := make([]preparedToolCall, len(calls))
	firstRunnableByKey := make(map[string]int)
	for i, call := range calls {
		prepared[i] = e.prepareToolCall(ctx, turn, assistantMsg, call)
		p := &prepared[i]
		if p.run == nil || !isCacheableTool(p.call.Name) {
			continue
		}
		key := dedupKey(p.call.Name, p.args)
		if j, seen := firstRunnableByKey[key]; seen {
			p.alsoWaitFor = j
		} else {
			firstRunnableByKey[key] = i
		}
	}

	sequential := execMode == models.ExecutionSequential
	if !sequential {
		for _, call := range calls {
			// switch_mode mutates agent state (the mode every other call's
			// guard reads), so a batch containing it must run sequentially.
			if call.Name == switchModeToolName {
				sequential = true
				break
			}
		}
	}
	if sequential {
		return e.runSequential(ctx, prepared)
	}
	return e.runScheduled(ctx, prepared)
}

// finalizeBatch reports the ordered results and whether every call asked to
// terminate the turn.
func finalizeBatch(results []models.AgentMessage) ([]models.AgentMessage, bool) {
	allTerminate := len(results) > 0
	for _, r := range results {
		if !isToolResultTerminate(r) {
			allTerminate = false
		}
	}
	return results, allTerminate
}

func (e *executor) runSequential(ctx context.Context, prepared []preparedToolCall) ([]models.AgentMessage, bool) {
	results := make([]models.AgentMessage, len(prepared))
	for i, p := range prepared {
		if p.run == nil {
			results[i] = p.resolved
		} else {
			results[i] = p.run(ctx)
		}
	}
	return finalizeBatch(results)
}

func (e *executor) runScheduled(ctx context.Context, prepared []preparedToolCall) ([]models.AgentMessage, bool) {
	accesses := make([][]tools.ToolAccess, len(prepared))
	for i, p := range prepared {
		accesses[i] = p.accesses
	}
	sched := newBatchScheduler(accesses)
	for i, p := range prepared {
		if p.alsoWaitFor >= 0 {
			sched.addWait(i, p.alsoWaitFor)
		}
	}

	results := make([]models.AgentMessage, len(prepared))
	var wg sync.WaitGroup
	for i, p := range prepared {
		if p.run == nil {
			// Short-circuited calls never execute: no side effects and
			// nothing for later calls to wait on.
			results[i] = p.resolved
			sched.finish(i)
			continue
		}
		wg.Add(1)
		go func(idx int, pc preparedToolCall) {
			defer wg.Done()
			defer sched.finish(idx)
			if err := sched.wait(ctx, idx); err != nil {
				results[idx] = e.makeToolResultMessage(pc.call,
					models.NewToolExecutionResultError("canceled before execution: "+err.Error()), true)
				return
			}
			results[idx] = pc.run(ctx)
		}(i, p)
	}
	wg.Wait()
	return finalizeBatch(results)
}
```

3c. `executeOneToolCall` 拆成 `prepareToolCall` + `runToolCall`(删除 `executeSequential`/`executeParallel`)。逻辑逐块搬运,不要改写语义:

```go
// prepareToolCall runs everything that must stay serial and ordered:
// meta-tool handling, validation, the path security guard, the permission
// chain (possibly interactive), and the before-hook. On any short-circuit
// the final tool_result is produced here and no ToolExecutionStart event is
// ever emitted. Approved calls return a runnable closure plus the resource
// accesses used for scheduling, computed from the final (post-hook) args.
func (e *executor) prepareToolCall(ctx context.Context, turn int, assistantMsg models.AgentMessage, call models.ToolCallContent) preparedToolCall {
	// Normalize arguments first so validation sees a non-nil map.
	args := call.Arguments
	if args == nil {
		args = make(map[string]any)
	}

	shortCircuit := func(result models.ToolExecutionResult, isError bool) preparedToolCall {
		return preparedToolCall{call: call, alsoWaitFor: -1, resolved: e.makeToolResultMessage(call, result, isError)}
	}

	// tool_search is a meta-tool resolved locally: it never reaches the registry.
	if call.Name == tools.ToolSearchName {
		return preparedToolCall{call: call, alsoWaitFor: -1, resolved: e.handleToolSearch(ctx, turn, assistantMsg, call)}
	}

	// switch_mode is a meta-tool that changes the agent mode for the next
	// turn. It still goes through the permission chain first: the
	// mode-transition guard may require user approval to leave a mode with
	// require_approval_to_exit set.
	if call.Name == switchModeToolName {
		info := ToolCallInfo{
			AssistantMessage: assistantMsg,
			ToolCall:         call,
			Args:             args,
			Context:          e.mgr.AllMessages(),
		}
		allowed, _, denyReason, confirmErr := e.confirmToolCall(ctx, turn, info)
		if confirmErr != nil || !allowed {
			reason := denyReason
			if confirmErr != nil {
				reason = confirmErr.Error()
			}
			if reason == "" {
				reason = "denied"
			}
			return shortCircuit(models.NewToolExecutionResultError(reason), true)
		}
		return preparedToolCall{call: call, alsoWaitFor: -1, resolved: e.handleSwitchMode(ctx, turn, assistantMsg, call)}
	}

	// Swarm exclusivity: a swarm subagent call (prompt_template + items) is
	// itself the concurrency unit and must be the only tool call in the
	// response (kimi-code's AgentSwarmExclusiveDeny). Mixed batches break
	// ordering assumptions, shared concurrency budgets, and result
	// attribution, so the call is refused with a corrective message; other
	// calls proceed.
	if e.batchLen > 1 && call.Name == "subagent" {
		if _, swarm := args["items"]; swarm {
			return shortCircuit(models.NewToolExecutionResultError(swarmExclusivityMessage), true)
		}
	}

	// Mode/skill tool-surface restrictions are enforced inside the permission
	// chain (guard policies, see guard_policies.go) rather than by filtering
	// the tool schemas: schema filtering would rebuild the tools array on
	// every switch, and tools are the first layer of the provider cache
	// prefix, so the whole conversation would be re-billed as fresh input.

	// Pre-execution argument validation. On failure we do NOT emit any tool
	// events: the failed attempt stays invisible in the live TUI, and the error
	// tool_result is fed back so the LLM can self-correct next turn.
	if exec, ok := e.registry.Get(call.Name); ok {
		if err := tools.ValidateArgs(exec.Definition(), args); err != nil {
			return shortCircuit(models.NewToolExecutionResultError(err.Error()), true)
		}
	}

	// Path security guard: resolves and validates file paths BEFORE the
	// permission check (mirrors Kimi Code's resolveExecution phase). Sensitive
	// files and relative-path workspace escapes are denied here so the model
	// receives an actionable error without ever triggering a user approval
	// prompt. The guard only validates; it does not rewrite args — each tool
	// still resolves its path via resolveInCwd as before.
	if rawPath, ok := args["path"].(string); ok && rawPath != "" {
		toolOp := pathOpForTool(call.Name)
		cwd, _ := os.Getwd()
		if _, err := builtin.ResolvePathAccess(rawPath, cwd, toolOp); err != nil {
			return shortCircuit(models.NewToolExecutionResultError(err.Error()), true)
		}
	}

	// Permission check: engine decision + optional interactive confirmation.
	info := ToolCallInfo{
		AssistantMessage: assistantMsg,
		ToolCall:         call,
		Args:             args,
		Context:          e.mgr.AllMessages(),
	}
	allowed, _, denyReason, confirmErr := e.confirmToolCall(ctx, turn, info)
	if confirmErr != nil || !allowed {
		reason := denyReason
		if confirmErr != nil {
			reason = confirmErr.Error()
		}
		if reason == "" {
			reason = "denied"
		}
		return shortCircuit(models.NewToolExecutionResultError(reason), true)
	}

	// Declarative before-tool hooks (e.g. extensions) run after permission approval.
	if e.cfg.BeforeToolCall != nil {
		beforeResult, err := e.cfg.BeforeToolCall(ctx, ToolCallInfo{
			AssistantMessage: assistantMsg,
			ToolCall:         call,
			Args:             args,
			Context:          e.mgr.AllMessages(),
		})
		if err != nil {
			return shortCircuit(models.NewToolExecutionResultError(err.Error()), true)
		}
		if beforeResult != nil && beforeResult.Block {
			return shortCircuit(models.NewToolExecutionResultError(beforeResult.Reason), true)
		}
		if beforeResult != nil && beforeResult.ModifiedArgs != nil {
			args = beforeResult.ModifiedArgs
			// Hook-rewritten args must pass the same schema validation as the
			// original arguments.
			if exec, ok := e.registry.Get(call.Name); ok {
				if err := tools.ValidateArgs(exec.Definition(), args); err != nil {
					return shortCircuit(models.NewToolExecutionResultError(err.Error()), true)
				}
			}
		}
	}

	// Resource accesses for the scheduler, from the final args. Tools that do
	// not declare accesses default to OpAll (serial with everything).
	accesses := []tools.ToolAccess{{Op: tools.OpAll}}
	if exec, ok := e.registry.Get(call.Name); ok {
		if declarer, ok := exec.(tools.AccessDeclarer); ok {
			if declared := declarer.DeclareAccesses(args); len(declared) > 0 {
				accesses = declared
			}
		}
	}

	return preparedToolCall{
		call:        call,
		args:        args,
		accesses:    accesses,
		alsoWaitFor: -1,
		run: func(runCtx context.Context) models.AgentMessage {
			return e.runToolCall(runCtx, turn, assistantMsg, call, args)
		},
	}
}

// runToolCall is the concurrent phase: dedup, execution, after-hook, and
// events. It only runs after prepareToolCall approved the call and the batch
// scheduler unblocked it.
func (e *executor) runToolCall(ctx context.Context, turn int, assistantMsg models.AgentMessage, call models.ToolCallContent, args map[string]any) models.AgentMessage {
	// Same-turn deduplication for read-only idempotent tools, keyed on the
	// final (post-hook) args. A dedup hit returns before ToolExecutionStart, so
	// duplicate calls never emit a Start without a matching End. The scheduler
	// orders same-batch duplicates after the original (alsoWaitFor edge), so
	// this lookup is deterministic even under concurrent execution.
	if isCacheableTool(call.Name) {
		key := dedupKey(call.Name, args)
		e.dedupMu.Lock()
		cached, ok := e.dedup[key]
		e.dedupMu.Unlock()
		if ok {
			return cloneAgentMessage(cached, call.ID)
		}
	}

	e.emitter.emit(ctx, events.ToolExecutionStartEvent{
		Base:       events.Base{Type: events.ToolExecutionStart, Turn: turn},
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Args:       call.Arguments,
	})

	result, isError := e.registry.Execute(ctx, call.ID, call.Name, args)

	// A successful use_skill call publishes the activated skill's restriction
	// via result details; adopt it for subsequent calls.
	if call.Name == skills.UseSkillToolName && !isError {
		e.updateSkillFilter(result.Details)
	}

	// Run after hook.
	if e.cfg.AfterToolCall != nil {
		afterResult, err := e.cfg.AfterToolCall(ctx, ToolCallResultInfo{
			AssistantMessage: assistantMsg,
			ToolCall:         call,
			Args:             args,
			Result:           result,
			IsError:          isError,
			Context:          e.mgr.AllMessages(),
		})
		if err != nil {
			result = models.NewToolExecutionResultError(err.Error())
			isError = true
		} else if afterResult != nil {
			if len(afterResult.Content) > 0 {
				result.Content = afterResult.Content
			}
			if afterResult.Details != nil {
				result.Details = afterResult.Details
			}
			if afterResult.IsError != nil {
				isError = *afterResult.IsError
			}
			result.Terminate = afterResult.Terminate
		}
	}

	// Reconcile task list when the model updates its plan.
	if call.Name == task.ToolName && e.taskMgr != nil && !isError {
		if raw, ok := args["todos"]; ok {
			if parsed, parseErr := task.Parse(raw); parseErr == nil {
				reconciled, warnings, _ := e.taskMgr.ReplaceAll(parsed)
				result = appendWarnings(result, warnings)
				e.emitter.emit(ctx, events.TaskListUpdatedEvent{
					Base:  events.Base{Type: events.TaskListUpdated, Turn: turn},
					Tasks: reconciled,
				})
			}
		}
	}

	e.emitter.emit(ctx, events.ToolExecutionEndEvent{
		Base:       events.Base{Type: events.ToolExecutionEnd, Turn: turn},
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Result:     result,
		IsError:    isError,
	})

	msg := e.makeToolResultMessage(call, result, isError)

	if isCacheableTool(call.Name) {
		key := dedupKey(call.Name, args)
		e.dedupMu.Lock()
		e.dedup[key] = msg
		e.dedupMu.Unlock()
	}

	e.emitter.emit(ctx, events.MessageStartEvent{
		Base:    events.Base{Type: events.MessageStart, Turn: turn},
		Message: msg,
	})
	e.emitter.emit(ctx, events.MessageEndEvent{
		Base:    events.Base{Type: events.MessageEnd, Turn: turn},
		Message: msg,
	})

	return msg
}
```

注意旧 `execute` 里这段要删除(由 OpAll 默认取代):

```go
			if exec, ok := e.registry.Get(call.Name); ok {
				if exec.Definition().ExecutionMode == models.ExecutionSequential {
					sequential = true
					break
				}
			}
```

- [ ] **Step 4: 运行全部 agent 测试**

Run: `go test ./pkg/agent -v 2>&1 | tail -30`
Expected: 全部 PASS(含已有的 swarm/dedup/switch/toolexec 测试——它们走 `e.execute` 或 agent 层,语义不变)

- [ ] **Step 5: 全量回归**

Run: `go build ./... && go test $(go list ./... | grep -v 'reference/Shannon') 2>&1 | tail -20`
Expected: 全部 PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/agent/executor.go pkg/agent/executor_scheduler_test.go
git commit -m "feat(agent): two-phase executor with conflict-based batch scheduling

Prepare (validation, path guard, permission, hooks) runs serially in
provider order; only pure execution overlaps, ordered by ToolAccess
conflicts. Same-batch duplicates of cacheable tools get a dedup edge so
their cache lookup is deterministic.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: 完全退役 `execution_mode`

`ExecutionMode` 类型、agent 级 `ToolExecutionMode`、`ToolDefinition.ExecutionMode` 字段、HTTP 工具 `execution_mode` 配置、checkpoint 字段——全部删除。调度器接管全部排序职责:批次永远走 `runScheduled`(OpAll 保证需要串行的工具仍然串行)。

`switch_mode` 的整批串行 veto 一并删除:mode 的消费(守卫确认)全部发生在串行 prepare 阶段,run 阶段没有任何 mode 读者(TUI 读 mode 有 `modeMu` 保护)。

**Files(按包分组,逐项删除):**

- `pkg/models/message.go` — 删 `ExecutionMode` 类型、`ExecutionParallel`/`ExecutionSequential` 常量、`ToolDefinition.ExecutionMode` 字段(约 309-322 行)
- `pkg/agent/loop.go` — 删 `Config.ToolExecutionMode` 字段(66 行附近);删 494 行 `execMode := a.cfg.ToolExecutionMode`,`a.executor.execute(ctx, turn, assistantMsg, toolCalls, execMode)` 改为 `a.executor.execute(ctx, turn, assistantMsg, toolCalls)`
- `pkg/agent/executor.go` — `execute` 签名删 `execMode models.ExecutionMode`;删 `sequential` 判定与 `runSequential` 调用,直接 `return e.runScheduled(ctx, prepared)`;删 `runSequential` 方法
- `pkg/agent/builder.go` — 删 32 行默认值与 55-58 行 `WithToolExecutionMode`
- `pkg/agent/checkpoint.go` — 删 56、99、139 行的 `ToolExecutionMode` 三处
- `pkg/checkpoint/checkpoint.go` — 删 49 行 `ToolExecutionMode` 字段(旧 checkpoint JSON 含此字段会被 unmarshal 忽略,无需迁移)
- `cmd/lcoder/main.go` — 删 354 行
- `pkg/agenthost/host.go` — 删 214 行
- `pkg/tools/http.go` — 删 21 行结构体字段与 38-46 行的 mode 映射,`Definition()` 不再设 `ExecutionMode`
- `pkg/tools/extensions.go` — 删 54-56 行映射
- `pkg/tools/mcp/tool.go` 实际路径是 `pkg/mcp/tool.go` — 删 41 行
- `pkg/config/config.go` — 删 22 行 `HTTPToolConfig.ExecutionMode`
- `pkg/config/hooks.go` — 删 42 行 `ExecutionMode` 字段
- `pkg/config/config_validate.go` — 删 84-90 行的 `execution_mode` 校验 switch
- `configs/lcoder.yaml` — 删 150 行的 `#     execution_mode: parallel` 注释行
- `pkg/tools/builtin/{bash,edit,find,grep,ls,read,skill,subagent,todo,write}.go` — 删各 `Definition()` 里的 `ExecutionMode: ...` 行

**测试更新(编译错误驱动,逐文件):**
- `pkg/agent/builder_test.go` — 删 31-40 行对 `WithToolExecutionMode` 的断言
- `pkg/agent/executor_test.go`、`toolexec_test.go`、`loop_test.go`、`loop_mode_priority_test.go` — 删所有 `ToolExecutionMode: models.ExecutionParallel/Sequential,` 字面量行
- `pkg/agent/executor_dedup_test.go` — 删 32 行;测试逻辑不变(addWait 边保证 dedup 仍确定性命中,`endCount==1` 保持成立)
- `pkg/agent/executor_swarm_test.go`、`executor_scheduler_test.go` — `e.execute(...)` 调用删最后一个 `models.ExecutionParallel` 实参
- `pkg/tools/registry_test.go` — 删 15、30、84 行定义里的 `ExecutionMode` 字段
- `pkg/tools/extensions_test.go` — 删 40-41、67、82-83 行的 ExecutionMode 断言与 67 行的配置字段
- `pkg/config/config_validate_test.go` — 删 `TestValidate_InvalidHTTPToolExecutionMode`(75-82 行附近)
- `pkg/tools/builtin/skill_test.go` — 删 42-43 行断言;`subagent_test.go` — 删 51-52 行断言

- [ ] **Step 1: 删除生产代码中的所有 `ExecutionMode` 用法**

按上面文件清单执行。删除后 `grep -rn "ExecutionMode\|execution_mode" --include="*.go" pkg/ cmd/ internal/` 应只剩测试文件的编译错误残留。

- [ ] **Step 2: 修测试直到编译通过**

Run: `go build ./... && go vet $(go list ./... | grep -v 'reference/Shannon') 2>&1 | head -30`
逐文件删除上清单中的测试残留,直到 vet 干净。
Expected: 无 `ExecutionMode` 相关错误;`grep -rn "ExecutionMode\|execution_mode" --include="*.go" pkg/ cmd/ internal/ | grep -v reference` 输出为空

- [ ] **Step 3: 全量测试**

Run: `go test $(go list ./... | grep -v 'reference/Shannon') 2>&1 | tail -20`
Expected: 全部 PASS

- [ ] **Step 4: race 复测 agent 包(调度器是并发代码)**

Run: `go test ./pkg/agent ./pkg/tools/... -race -count=1 2>&1 | tail -10`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "refactor: retire ExecutionMode entirely in favor of resource scheduling

Tool-level sequential labels and the agent-level parallel/sequential
switch are subsumed by ToolAccess conflict scheduling: undeclared tools
default to OpAll and serialize against everything, declared tools overlap
when their resources do not conflict.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6: 文档与收尾

**Files:**
- Modify: `docs/tool-concurrency-optimization.md`
- Modify: `CLAUDE.md`

- [ ] **Step 1: 更新设计文档状态**

`docs/tool-concurrency-optimization.md` 第 3 行改为:

```
> 状态: 已实施 | 参考: Kimi Code `ToolAccesses` + `ToolScheduler`
```

并在"改造方案"开头补一行实施注记:`switch_mode` 整批串行 veto 已随 ExecutionMode 一并移除(mode 消费全部在串行 prepare 阶段);同批次重复读通过调度器 addWait 边保证 dedup 确定性。

- [ ] **Step 2: 更新 CLAUDE.md 的 executor 描述**

`CLAUDE.md` 中 `executor`(`pkg/agent/executor.go`)一行改为:

```
- `executor` (`pkg/agent/executor.go`) — two-phase tool execution: serial preparation (validation, path guard, permission prompts, hooks) in provider order, then conflict-scheduled concurrent execution via `ToolAccess` declarations (`scheduler.go`); owns deferred tool promotion via `tool_search`.
```

- [ ] **Step 3: 最终全量验证**

Run: `go build ./... && go vet $(go list ./... | grep -v 'reference/Shannon') && go test $(go list ./... | grep -v 'reference/Shannon') -count=1 2>&1 | tail -10`
Expected: 全绿

- [ ] **Step 4: Commit**

```bash
git add docs/tool-concurrency-optimization.md CLAUDE.md
git commit -m "docs: mark tool concurrency optimization as implemented

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Self-Review 记录

- **Spec 覆盖**:设计文档的 ToolAccess 接口(Task 1)、AccessDeclarer(Task 2)、调度器(Task 3)、两阶段拆分(Task 4)、ExecutionMode 退役(Task 5,用户追加要求"完全退役")均有对应任务。影响评估表中的"未实现接口保持串行"由 OpAll 默认覆盖(Task 4 Step 3c + 测试 TestExecutorUndeclaredToolDefaultsToAll)。
- **类型一致性**:`preparedToolCall.alsoWaitFor` 在所有构造点显式初始化为 -1(shortCircuit、两个 meta-tool 返回、runnable 返回);`batchScheduler` 的 `wait/finish/addWait` 命名在 Task 3 定义、Task 4 使用一致;`schedCall`/`waitForStarts`/`newSchedulerTestExecutor` 等测试辅助在 Task 4 单文件内自洽。`gateTool` 不实现 `AccessDeclarer`(供"默认 OpAll"测试),`declaringGateTool` 包装实现——两测试分工明确。
- **占位符扫描**:Task 5 的测试更新以"编译错误驱动"列出具体文件与行号,无 TBD;所有新增代码均给出完整实现。
- **已知取舍**:同批次内权限确认在执行前完成(批准时机略提前),已在设计文档记录;`ls` 的 Recursive 声明对深层写入过度保守,可接受。
