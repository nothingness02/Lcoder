# Sandbox Integration (Plan 2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把已合并的 `pkg/sandbox` 接入工具系统与启动装配链,让 bash/file/http 工具在 sandbox 策略下执行,默认 passthrough 零回归。

**Architecture:** 通过可选接口 `SandboxAware{UseSandbox(sb)}` 注入——`Registry.Register` 在注册时探测并注入,敏感工具(bash/6 个 file 工具/http)实现该接口,第三方工具不实现则零影响。registry 通过 `SetSandbox` 持有 sandbox(而非改 `NewRegistry` 签名,以免破坏 10 个现有测试调用点;注入机制与 spec §3 一致)。装配在 `cmd/lcoder/prepareAgent` 构造 sandbox 并注入 registry。

**Tech Stack:** Go 1.25.4, `pkg/sandbox`(已实现), koanf/yaml.v3 配置, `pkg/tools` 工具系统。

**Spec:** `docs/superpowers/specs/2026-06-30-sandbox-integration-design.md`

---

## File Structure

**新建:**
- `pkg/tools/sandbox_inject_test.go` — Task 1 注入机制测试
- `pkg/tools/builtin/fspath.go` — Task 3 file 工具共享路径校验 helper
- `pkg/tools/builtin/fspath_test.go` — Task 3 helper + file 工具拒绝测试
- `cmd/lcoder/sandbox.go` — Task 6 `toSandboxConfig` 映射函数
- `cmd/lcoder/sandbox_test.go` — Task 6 映射函数测试
- `test/integration/sandbox_integration_test.go` — Task 6 端到端零回归测试

**修改:**
- `pkg/tools/base.go` — 加 `SandboxAware` 接口(Task 1)
- `pkg/tools/registry.go` — 加 `sb` 字段 + `SetSandbox` + `Register` 探测注入(Task 1)
- `pkg/tools/builtin/bash.go` — 加 `sb` 字段 + `UseSandbox` + `Execute` 走 `sb.Exec`(Task 2)
- `pkg/tools/builtin/bash_sandbox_test.go` — 新建,bash 接入测试(Task 2)
- `pkg/tools/builtin/{read,write,edit,ls,find,grep}.go` — 加 `sb` 字段 + `UseSandbox` + 用 helper(Task 3)
- `pkg/tools/http.go` — 加 `UseSandbox`(Task 4)
- `pkg/tools/http_sandbox_test.go` — 新建,http 注入测试(Task 4)
- `pkg/config/config.go` — 加 `SandboxConfig` 等结构 + 顶层字段(Task 5)
- `pkg/config/sandbox_config_test.go` — 新建,yaml 解析测试(Task 5)
- `cmd/lcoder/main.go` — `prepareAgent` 装配 sandbox(Task 6)
- `configs/lcoder.yaml` — 加注释化 sandbox 段(Task 6)

---

## Task 1: SandboxAware 接口 + Registry 注入机制

**Files:**
- Modify: `pkg/tools/base.go`
- Modify: `pkg/tools/registry.go`
- Test: `pkg/tools/sandbox_inject_test.go`

- [ ] **Step 1: Write the failing test**

Create `pkg/tools/sandbox_inject_test.go`:

```go
package tools

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/sandbox"
)

type fakeAwareTool struct{ got sandbox.Sandbox }

func (f *fakeAwareTool) Definition() models.ToolDefinition {
	return models.ToolDefinition{Name: "aware"}
}
func (f *fakeAwareTool) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolResult, error) {
	return models.ToolResult{}, nil
}
func (f *fakeAwareTool) UseSandbox(sb sandbox.Sandbox) { f.got = sb }

type plainTool struct{}

func (plainTool) Definition() models.ToolDefinition {
	return models.ToolDefinition{Name: "plain"}
}
func (plainTool) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolResult, error) {
	return models.ToolResult{}, nil
}

func TestRegisterInjectsSandboxIntoAwareTool(t *testing.T) {
	r := NewRegistry(".")
	sb := sandbox.NewFakeSandbox()
	r.SetSandbox(sb)
	tool := &fakeAwareTool{}
	r.Register("aware", tool)
	if tool.got != sb {
		t.Fatalf("expected sandbox injected, got %v", tool.got)
	}
}

func TestRegisterSkipsPlainTool(t *testing.T) {
	r := NewRegistry(".")
	r.SetSandbox(sandbox.NewFakeSandbox())
	r.Register("plain", plainTool{}) // must not panic
}

func TestRegisterNilSandboxNoPanic(t *testing.T) {
	r := NewRegistry(".")
	r.Register("aware", &fakeAwareTool{}) // sb nil, must not panic
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tools/ -run TestRegister -v`
Expected: FAIL — compile error `r.SetSandbox undefined` and `SandboxAware` not used.

- [ ] **Step 3: Add SandboxAware interface to base.go**

Edit `pkg/tools/base.go` — add the import and interface:

```go
package tools

import (
	"context"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/sandbox"
)

// Executable is the interface implemented by every tool available to the agent.
type Executable interface {
	Definition() models.ToolDefinition
	Execute(ctx context.Context, callID string, args map[string]any) (models.ToolResult, error)
}

// Factory creates a tool instance bound to a working directory.
type Factory func(cwd string) Executable

// SandboxAware is optionally implemented by tools that need a sandbox. The
// Registry detects it at registration time and injects the active sandbox.
// Tools that do not implement it (e.g. third-party extensions) are unaffected.
type SandboxAware interface {
	UseSandbox(sb sandbox.Sandbox)
}
```

- [ ] **Step 4: Add sb field, SetSandbox, and Register probe to registry.go**

Edit `pkg/tools/registry.go`. Change the import block, the struct, and the `Register` method:

```go
import (
	"context"
	"fmt"
	"sync"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/sandbox"
)

// Registry holds all available tools.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Executable
	cwd   string
	sb    sandbox.Sandbox
}
```

Add a `SetSandbox` method (place it right after `NewRegistry`):

```go
// SetSandbox sets the sandbox injected into SandboxAware tools at registration.
// Call before registering tools so subsequent Register calls inject it.
func (r *Registry) SetSandbox(sb sandbox.Sandbox) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sb = sb
}
```

Replace the `Register` method body to probe and inject:

```go
// Register adds a tool to the registry, injecting the sandbox if the tool
// implements SandboxAware and a sandbox is set.
func (r *Registry) Register(name string, exec Executable) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[name] = exec
	if r.sb != nil {
		if sa, ok := exec.(SandboxAware); ok {
			sa.UseSandbox(r.sb)
		}
	}
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./pkg/tools/ -run TestRegister -v`
Expected: PASS (3 tests).

- [ ] **Step 6: Verify no regression across tools package**

Run: `go test ./pkg/tools/... && go vet ./pkg/tools/...`
Expected: PASS, no vet errors.

- [ ] **Step 7: Commit**

```bash
git add pkg/tools/base.go pkg/tools/registry.go pkg/tools/sandbox_inject_test.go
git commit -m "feat(tools): SandboxAware interface and registry injection"
```

---

## Task 2: Bash 接入 sb.Exec

**Files:**
- Modify: `pkg/tools/builtin/bash.go`
- Test: `pkg/tools/builtin/bash_sandbox_test.go`

- [ ] **Step 1: Write the failing test**

Create `pkg/tools/builtin/bash_sandbox_test.go`:

```go
package builtin

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/sandbox"
)

func TestBashUsesSandboxExec(t *testing.T) {
	b := NewBash("/tmp").(*Bash)
	fake := sandbox.NewFakeSandbox()
	fake.Result = sandbox.ExecResult{Stdout: "hello"}
	b.UseSandbox(fake)

	res, err := b.Execute(context.Background(), "c1", map[string]any{"command": "echo hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.Calls) != 1 {
		t.Fatalf("expected 1 exec call, got %d", len(fake.Calls))
	}
	if fake.Calls[0].Command != "echo hello" {
		t.Fatalf("command = %q", fake.Calls[0].Command)
	}
	txt := res.Content[0].(models.TextContent).Text
	if txt != "hello" {
		t.Fatalf("output = %q, want %q", txt, "hello")
	}
}

func TestBashSandboxNonZeroExitReturnsError(t *testing.T) {
	b := NewBash("/tmp").(*Bash)
	fake := sandbox.NewFakeSandbox()
	fake.Result = sandbox.ExecResult{Stderr: "boom", ExitCode: 1}
	b.UseSandbox(fake)

	_, err := b.Execute(context.Background(), "c1", map[string]any{"command": "false"})
	if err == nil {
		t.Fatal("expected error for non-zero exit")
	}
}

func TestBashSandboxTimeoutMarksOutput(t *testing.T) {
	b := NewBash("/tmp").(*Bash)
	fake := sandbox.NewFakeSandbox()
	fake.Result = sandbox.ExecResult{Stdout: "partial", TimedOut: true}
	b.UseSandbox(fake)

	res, err := b.Execute(context.Background(), "c1", map[string]any{"command": "sleep 99"})
	if err == nil {
		t.Fatal("expected error on timeout")
	}
	txt := res.Content[0].(models.TextContent).Text
	if txt != "partial\n[command timed out]" {
		t.Fatalf("output = %q", txt)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tools/builtin/ -run TestBashUsesSandboxExec -v`
Expected: FAIL — `b.UseSandbox undefined` / `Bash` has no field `sb`.

- [ ] **Step 3: Modify bash.go — add sb field, UseSandbox, route Execute through sb.Exec**

Edit `pkg/tools/builtin/bash.go`. Change imports to add `sandbox`, change the struct, add `UseSandbox`, and replace the exec block in `Execute`.

New imports block:

```go
import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/sandbox"
	"github.com/lcoder/lcoder/pkg/tools"
)
```

New struct + UseSandbox:

```go
// Bash executes shell commands.
type Bash struct {
	cwd string
	sb  sandbox.Sandbox
}

// NewBash creates a bash tool.
func NewBash(cwd string) tools.Executable {
	return &Bash{cwd: cwd}
}

// UseSandbox injects the sandbox used to run commands.
func (b *Bash) UseSandbox(sb sandbox.Sandbox) { b.sb = sb }
```

In `Execute`, replace the block from `shell := os.Getenv("SHELL")` through the final `return ..., nil` (current lines 67-94) with:

```go
	// Sandboxed path: route the command through the sandbox backend.
	if b.sb != nil {
		result, execErr := b.sb.Exec(ctx, sandbox.ExecSpec{
			Command: command,
			Cwd:     cwd,
			Env:     os.Environ(),
			Timeout: time.Duration(timeout) * time.Second,
		})
		output := result.Combined()
		if result.TimedOut {
			output += "\n[command timed out]"
		}
		res := models.ToolResult{
			Content: []models.ContentPart{models.TextContent{Text: strings.TrimSpace(output)}},
			Details: map[string]any{"command": command, "cwd": cwd},
		}
		if execErr != nil {
			return res, fmt.Errorf("command failed: %w", execErr)
		}
		if result.TimedOut {
			return res, fmt.Errorf("command failed: timed out")
		}
		if result.ExitCode != 0 {
			return res, fmt.Errorf("command failed: exit code %d", result.ExitCode)
		}
		return res, nil
	}

	// Fallback path (no sandbox injected, e.g. unit tests): historical behavior.
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "sh"
	}

	cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, shell, "-c", command)
	cmd.Dir = cwd
	cmd.Env = os.Environ()

	out, err := cmd.CombinedOutput()
	output := string(out)
	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			output += "\n[command timed out]"
		}
		return models.ToolResult{
			Content: []models.ContentPart{models.TextContent{Text: strings.TrimSpace(output)}},
			Details: map[string]any{"command": command, "cwd": cwd},
		}, fmt.Errorf("command failed: %w", err)
	}

	return models.ToolResult{
		Content: []models.ContentPart{models.TextContent{Text: strings.TrimSpace(output)}},
		Details: map[string]any{"command": command, "cwd": cwd},
	}, nil
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/tools/builtin/ -run TestBash -v`
Expected: PASS (all TestBash* tests including pre-existing ones).

- [ ] **Step 5: Verify no regression**

Run: `go test ./pkg/tools/builtin/... && go vet ./pkg/tools/builtin/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/tools/builtin/bash.go pkg/tools/builtin/bash_sandbox_test.go
git commit -m "feat(tools): bash routes execution through sandbox"
```

---

## Task 3: File 工具共享 helper + 6 工具接入 Check

**Files:**
- Create: `pkg/tools/builtin/fspath.go`
- Create: `pkg/tools/builtin/fspath_test.go`
- Modify: `pkg/tools/builtin/{read,write,edit,ls,find,grep}.go`

- [ ] **Step 1: Write the failing test for the helper**

Create `pkg/tools/builtin/fspath_test.go`:

```go
package builtin

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/lcoder/lcoder/pkg/sandbox"
)

// denyFS is a FilesystemPolicy that rejects every path. Used to verify tools
// honor sb.Filesystem().Check.
type denyFS struct{}

func (denyFS) Check(path string, op sandbox.FSOp) error {
	return fmt.Errorf("denied: %s", path)
}
func (denyFS) SubprocessMounts() []sandbox.Mount { return nil }

func denyingSandbox() *sandbox.FakeSandbox {
	f := sandbox.NewFakeSandbox()
	f.FSPolicy = denyFS{}
	return f
}

func TestResolveAndCheckAllowed(t *testing.T) {
	got, err := resolveAndCheck("/proj", sandbox.NewFakeSandbox(), "x.txt", sandbox.FSRead)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Clean("/proj/x.txt")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestResolveAndCheckDenied(t *testing.T) {
	_, err := resolveAndCheck("/proj", denyingSandbox(), "x.txt", sandbox.FSWrite)
	if err == nil {
		t.Fatal("expected denial error")
	}
}

func TestResolveAndCheckNilSandbox(t *testing.T) {
	got, err := resolveAndCheck("/proj", nil, "/abs/x.txt", sandbox.FSRead)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != filepath.Clean("/abs/x.txt") {
		t.Fatalf("got %q", got)
	}
}

func TestReadDeniedBySandbox(t *testing.T) {
	r := NewRead("/project").(*Read)
	r.UseSandbox(denyingSandbox())
	if _, err := r.Execute(context.Background(), "c", map[string]any{"path": "secret.txt"}); err == nil {
		t.Fatal("expected denial error from read")
	}
}

func TestWriteDeniedBySandbox(t *testing.T) {
	w := NewWrite("/project").(*Write)
	w.UseSandbox(denyingSandbox())
	if _, err := w.Execute(context.Background(), "c", map[string]any{"path": "out.txt", "content": "x"}); err == nil {
		t.Fatal("expected denial error from write")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tools/builtin/ -run TestResolveAndCheck -v`
Expected: FAIL — `resolveAndCheck` undefined, `Read`/`Write` have no `UseSandbox`.

- [ ] **Step 3: Create the helper**

Create `pkg/tools/builtin/fspath.go`:

```go
package builtin

import (
	"path/filepath"

	"github.com/lcoder/lcoder/pkg/sandbox"
)

// resolveAndCheck resolves rawPath against cwd (absolutizing + cleaning) and, if
// a sandbox is present, enforces sb.Filesystem().Check for the given op. It
// returns the cleaned absolute path or the policy error.
func resolveAndCheck(cwd string, sb sandbox.Sandbox, rawPath string, op sandbox.FSOp) (string, error) {
	path := rawPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	path = filepath.Clean(path)
	if sb != nil {
		if err := sb.Filesystem().Check(path, op); err != nil {
			return "", err
		}
	}
	return path, nil
}
```

- [ ] **Step 4: Modify read.go**

Add `sandbox` import, `sb` field, `UseSandbox`, and use the helper. Replace struct + constructor:

```go
// Read reads files with optional offset/limit.
type Read struct {
	cwd string
	sb  sandbox.Sandbox
}

// NewRead creates a read tool.
func NewRead(cwd string) tools.Executable {
	return &Read{cwd: cwd}
}

// UseSandbox injects the filesystem policy enforced before reads.
func (r *Read) UseSandbox(sb sandbox.Sandbox) { r.sb = sb }
```

Add `"github.com/lcoder/lcoder/pkg/sandbox"` to the import block. In `Execute`, replace the path-resolution lines (current 55-58: the `if !filepath.IsAbs` block and `path = filepath.Clean(path)`) with:

```go
	path, err := resolveAndCheck(r.cwd, r.sb, path, sandbox.FSRead)
	if err != nil {
		return models.ToolResult{}, err
	}
```

(The subsequent `info, err := os.Stat(path)` reuses `err` via `:=` since `info` is new — valid Go.)

- [ ] **Step 5: Modify write.go**

Add `sandbox` import. Replace struct + constructor + add UseSandbox:

```go
// Write writes content to a file.
type Write struct {
	cwd string
	sb  sandbox.Sandbox
}

// NewWrite creates a write tool.
func NewWrite(cwd string) tools.Executable {
	return &Write{cwd: cwd}
}

// UseSandbox injects the filesystem policy enforced before writes.
func (w *Write) UseSandbox(sb sandbox.Sandbox) { w.sb = sb }
```

In `Execute`, replace the path-resolution lines (current 54-57) with:

```go
	path, err := resolveAndCheck(w.cwd, w.sb, path, sandbox.FSWrite)
	if err != nil {
		return models.ToolResult{}, err
	}
```

(The following `if err := os.MkdirAll(...)` becomes `if err = os.MkdirAll(...)` — change `:=` to `=` since `err` is now declared. Likewise `if err := os.WriteFile(...)` → `if err = os.WriteFile(...)`.)

- [ ] **Step 6: Modify edit.go**

Add `sandbox` import. Replace struct + constructor + add UseSandbox:

```go
// Edit performs exact-text replacements in a file.
type Edit struct {
	cwd string
	sb  sandbox.Sandbox
}

// NewEdit creates an edit tool.
func NewEdit(cwd string) tools.Executable {
	return &Edit{cwd: cwd}
}

// UseSandbox injects the filesystem policy enforced before edits.
func (e *Edit) UseSandbox(sb sandbox.Sandbox) { e.sb = sb }
```

In `Execute`, replace the path-resolution lines (current 64-67) with `FSWrite` (edit ultimately writes):

```go
	path, err := resolveAndCheck(e.cwd, e.sb, path, sandbox.FSWrite)
	if err != nil {
		return models.ToolResult{}, err
	}
```

(The following `data, err := os.ReadFile(path)` reuses `err` via `:=` since `data` is new — valid.)

- [ ] **Step 7: Modify ls.go**

Add `sandbox` import. Replace struct + constructor + add UseSandbox:

```go
// Ls lists directory contents.
type Ls struct {
	cwd string
	sb  sandbox.Sandbox
}

// NewLs creates an ls tool.
func NewLs(cwd string) tools.Executable {
	return &Ls{cwd: cwd}
}

// UseSandbox injects the filesystem policy enforced before listing.
func (l *Ls) UseSandbox(sb sandbox.Sandbox) { l.sb = sb }
```

In `Execute`, replace the path-resolution lines (current 46-49) with:

```go
	path, err := resolveAndCheck(l.cwd, l.sb, path, sandbox.FSRead)
	if err != nil {
		return models.ToolResult{}, err
	}
```

(The following `entries, err := os.ReadDir(path)` reuses `err` via `:=` since `entries` is new — valid.)

- [ ] **Step 8: Modify find.go**

Add `sandbox` import. Replace struct + constructor + add UseSandbox:

```go
// Find searches for files by name pattern.
type Find struct {
	cwd string
	sb  sandbox.Sandbox
}

// NewFind creates a find tool.
func NewFind(cwd string) tools.Executable {
	return &Find{cwd: cwd}
}

// UseSandbox injects the filesystem policy enforced during traversal.
func (f *Find) UseSandbox(sb sandbox.Sandbox) { f.sb = sb }
```

In `Execute`, replace the path-resolution lines (current 56-59) with:

```go
	path, err := resolveAndCheck(f.cwd, f.sb, path, sandbox.FSRead)
	if err != nil {
		return models.ToolResult{}, err
	}
```

Then in the `WalkDir` callback, add a per-child Check right after the `if d.IsDir()` guard (so out-of-bounds children are skipped). The callback becomes:

```go
	err = filepath.WalkDir(path, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if f.sb != nil {
			if cerr := f.sb.Filesystem().Check(p, sandbox.FSRead); cerr != nil {
				return nil // skip out-of-bounds child
			}
		}
		matched, _ := filepath.Match(pattern, filepath.Base(p))
		if matched {
			rel, _ := filepath.Rel(f.cwd, p)
			matches = append(matches, rel)
		}
		return nil
	})
```

(Note the inner error param renamed `err`→`walkErr` to avoid shadowing the outer `err` from `resolveAndCheck`.)

- [ ] **Step 9: Modify grep.go**

Add `sandbox` import. Replace struct + constructor + add UseSandbox:

```go
// Grep searches file contents for a pattern.
type Grep struct {
	cwd string
	sb  sandbox.Sandbox
}

// NewGrep creates a grep tool.
func NewGrep(cwd string) tools.Executable {
	return &Grep{cwd: cwd}
}

// UseSandbox injects the filesystem policy enforced during traversal.
func (g *Grep) UseSandbox(sb sandbox.Sandbox) { g.sb = sb }
```

In `Execute`, replace the path-resolution lines (current 60-63) with:

```go
	path, err := resolveAndCheck(g.cwd, g.sb, path, sandbox.FSRead)
	if err != nil {
		return models.ToolResult{}, err
	}
```

Then in the `WalkDir` callback, add a per-child Check before `os.ReadFile`. The callback becomes:

```go
	err = filepath.WalkDir(path, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if glob != "" {
			matched, _ := filepath.Match(glob, filepath.Base(p))
			if !matched {
				return nil
			}
		}
		if g.sb != nil {
			if cerr := g.sb.Filesystem().Check(p, sandbox.FSRead); cerr != nil {
				return nil // skip out-of-bounds child
			}
		}
		data, readErr := os.ReadFile(p)
		if readErr != nil {
			return nil
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if strings.Contains(line, pattern) {
				rel, _ := filepath.Rel(g.cwd, p)
				matches = append(matches, fmt.Sprintf("%s:%d:%s", rel, i+1, line))
			}
		}
		return nil
	})
```

(Inner error param renamed `err`→`walkErr`; the `os.ReadFile` error renamed to `readErr` to avoid shadowing.)

- [ ] **Step 10: Run tests to verify they pass**

Run: `go test ./pkg/tools/builtin/ -run 'TestResolveAndCheck|TestRead|TestWrite' -v`
Expected: PASS.

- [ ] **Step 11: Verify no regression across builtin package**

Run: `go test ./pkg/tools/builtin/... && go vet ./pkg/tools/builtin/...`
Expected: PASS (all pre-existing read/write/edit/ls/find/grep tests still green).

- [ ] **Step 12: Commit**

```bash
git add pkg/tools/builtin/fspath.go pkg/tools/builtin/fspath_test.go pkg/tools/builtin/read.go pkg/tools/builtin/write.go pkg/tools/builtin/edit.go pkg/tools/builtin/ls.go pkg/tools/builtin/find.go pkg/tools/builtin/grep.go
git commit -m "feat(tools): file tools enforce sandbox filesystem checks"
```

---

## Task 4: HTTP 工具注入 DialContext

**Files:**
- Modify: `pkg/tools/http.go`
- Test: `pkg/tools/http_sandbox_test.go`

- [ ] **Step 1: Write the failing test**

Create `pkg/tools/http_sandbox_test.go`:

```go
package tools

import (
	"net/http"
	"testing"

	"github.com/lcoder/lcoder/pkg/sandbox"
)

func TestHTTPUseSandboxSetsTransport(t *testing.T) {
	h := NewHTTPExecutable(HTTPConfig{Name: "x"})
	h.UseSandbox(sandbox.NewFakeSandbox())

	tr, ok := h.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", h.client.Transport)
	}
	if tr.DialContext == nil {
		t.Fatal("expected DialContext to be set from sandbox network policy")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tools/ -run TestHTTPUseSandbox -v`
Expected: FAIL — `h.UseSandbox undefined`.

- [ ] **Step 3: Add UseSandbox to http.go**

Edit `pkg/tools/http.go`. Add the `sandbox` import to the import block:

```go
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/sandbox"
```

Add the method right after `NewHTTPExecutable`:

```go
// UseSandbox routes the tool's HTTP client through the sandbox network policy.
func (h *HTTPExecutable) UseSandbox(sb sandbox.Sandbox) {
	h.client = &http.Client{
		Transport: &http.Transport{DialContext: sb.Network().DialContext},
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/tools/ -run TestHTTPUseSandbox -v`
Expected: PASS.

- [ ] **Step 5: Verify no regression**

Run: `go test ./pkg/tools/... && go vet ./pkg/tools/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/tools/http.go pkg/tools/http_sandbox_test.go
git commit -m "feat(tools): http tool dials through sandbox network policy"
```

---

## Task 5: Config — SandboxConfig 结构与 yaml 解析

**Files:**
- Modify: `pkg/config/config.go`
- Test: `pkg/config/sandbox_config_test.go`

- [ ] **Step 1: Write the failing test**

Create `pkg/config/sandbox_config_test.go`:

```go
package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSandboxConfigParses(t *testing.T) {
	data := []byte(`
sandbox:
  backend: soft-limit
  env_allowlist: [PATH, HOME]
  network:
    default: deny
    allow: ["api.github.com:443"]
  filesystem:
    writable: ["."]
    readable: ["."]
  limits:
    max_memory_mb: 256
    max_cpu_seconds: 30
    max_output_bytes: 1048576
`)
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Sandbox.Backend != "soft-limit" {
		t.Fatalf("backend = %q", cfg.Sandbox.Backend)
	}
	if cfg.Sandbox.Network.Default != "deny" {
		t.Fatalf("network.default = %q", cfg.Sandbox.Network.Default)
	}
	if len(cfg.Sandbox.Network.Allow) != 1 || cfg.Sandbox.Network.Allow[0] != "api.github.com:443" {
		t.Fatalf("network.allow = %v", cfg.Sandbox.Network.Allow)
	}
	if len(cfg.Sandbox.EnvAllowlist) != 2 {
		t.Fatalf("env_allowlist = %v", cfg.Sandbox.EnvAllowlist)
	}
	if cfg.Sandbox.Limits.MaxMemoryMB != 256 {
		t.Fatalf("limits.max_memory_mb = %d", cfg.Sandbox.Limits.MaxMemoryMB)
	}
}

func TestSandboxConfigDefaultsEmpty(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Sandbox.Backend != "" {
		t.Fatalf("expected empty backend (passthrough), got %q", cfg.Sandbox.Backend)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/config/ -run TestSandboxConfig -v`
Expected: FAIL — `cfg.Sandbox` undefined.

- [ ] **Step 3: Add SandboxConfig types and the top-level field**

Edit `pkg/config/config.go`. Add these types just above the `Config` struct (after `PermissionConfig`):

```go
// SandboxConfig configures the sandbox backend wiring tools at startup.
type SandboxConfig struct {
	Backend      string                  `yaml:"backend"` // "" -> passthrough
	EnvAllowlist []string                `yaml:"env_allowlist"`
	Network      SandboxNetworkConfig    `yaml:"network"`
	Filesystem   SandboxFilesystemConfig `yaml:"filesystem"`
	Limits       SandboxLimitsConfig     `yaml:"limits"`
}

// SandboxNetworkConfig is the yaml form of the network allowlist.
type SandboxNetworkConfig struct {
	Default string   `yaml:"default"` // "deny" | "allow"
	Allow   []string `yaml:"allow"`
}

// SandboxFilesystemConfig lists allowed roots (relative to project root).
type SandboxFilesystemConfig struct {
	Readable []string `yaml:"readable"`
	Writable []string `yaml:"writable"`
}

// SandboxLimitsConfig is the yaml form of resource limits.
type SandboxLimitsConfig struct {
	MaxMemoryMB    int `yaml:"max_memory_mb"`
	MaxCPUSeconds  int `yaml:"max_cpu_seconds"`
	MaxOutputBytes int `yaml:"max_output_bytes"`
}
```

Add the field to the `Config` struct (after the `Providers` line, before the `Catalog` block):

```go
	Sandbox     SandboxConfig           `yaml:"sandbox"`
```

`DefaultConfig()` needs no change — the zero-value `Sandbox.Backend` is `""` (passthrough).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/config/ -run TestSandboxConfig -v`
Expected: PASS (both tests).

- [ ] **Step 5: Verify no regression**

Run: `go test ./pkg/config/... && go vet ./pkg/config/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/config/config.go pkg/config/sandbox_config_test.go
git commit -m "feat(config): add sandbox configuration section"
```

---

## Task 6: 装配 — 映射函数 + prepareAgent 注入 + 集成测试

**Files:**
- Create: `cmd/lcoder/sandbox.go`
- Create: `cmd/lcoder/sandbox_test.go`
- Modify: `cmd/lcoder/main.go`
- Create: `test/integration/sandbox_integration_test.go`
- Modify: `configs/lcoder.yaml`

- [ ] **Step 1: Write the failing test for the mapping function**

Create `cmd/lcoder/sandbox_test.go`:

```go
package main

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/config"
)

func TestToSandboxConfigMapsFields(t *testing.T) {
	c := config.SandboxConfig{
		Backend:      "soft-limit",
		EnvAllowlist: []string{"PATH"},
		Network:      config.SandboxNetworkConfig{Default: "deny", Allow: []string{"api.github.com:443"}},
		Filesystem:   config.SandboxFilesystemConfig{Readable: []string{"."}, Writable: []string{"."}},
		Limits:       config.SandboxLimitsConfig{MaxMemoryMB: 128, MaxCPUSeconds: 10, MaxOutputBytes: 4096},
	}
	got := toSandboxConfig(c, "/project/root")

	if got.Backend != "soft-limit" {
		t.Fatalf("backend = %q", got.Backend)
	}
	if got.ProjectRoot != "/project/root" {
		t.Fatalf("projectRoot = %q", got.ProjectRoot)
	}
	if got.Network.DefaultAllow {
		t.Fatal("default: deny should map to DefaultAllow=false")
	}
	if len(got.Network.Allow) != 1 || got.Network.Allow[0] != "api.github.com:443" {
		t.Fatalf("network.allow = %v", got.Network.Allow)
	}
	if got.Limits.MaxMemoryMB != 128 {
		t.Fatalf("limits.maxMemoryMB = %d", got.Limits.MaxMemoryMB)
	}
}

func TestToSandboxConfigDefaultAllow(t *testing.T) {
	got := toSandboxConfig(config.SandboxConfig{Network: config.SandboxNetworkConfig{Default: "allow"}}, "/r")
	if !got.Network.DefaultAllow {
		t.Fatal("default: allow should map to DefaultAllow=true")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/lcoder/ -run TestToSandboxConfig -v`
Expected: FAIL — `toSandboxConfig` undefined.

- [ ] **Step 3: Create the mapping function**

Create `cmd/lcoder/sandbox.go`:

```go
package main

import (
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/sandbox"
)

// toSandboxConfig maps the yaml-facing config.SandboxConfig into a
// sandbox.Config, injecting projectRoot as the base for relative roots.
func toSandboxConfig(c config.SandboxConfig, projectRoot string) sandbox.Config {
	return sandbox.Config{
		Backend:      c.Backend,
		EnvAllowlist: c.EnvAllowlist,
		Network: sandbox.NetworkConfig{
			DefaultAllow: c.Network.Default == "allow",
			Allow:        c.Network.Allow,
		},
		Filesystem: sandbox.FilesystemConfig{
			Readable: c.Filesystem.Readable,
			Writable: c.Filesystem.Writable,
		},
		Limits: sandbox.ResourceLimits{
			MaxMemoryMB:    c.Limits.MaxMemoryMB,
			MaxCPUSeconds:  c.Limits.MaxCPUSeconds,
			MaxOutputBytes: c.Limits.MaxOutputBytes,
		},
		ProjectRoot: projectRoot,
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/lcoder/ -run TestToSandboxConfig -v`
Expected: PASS.

- [ ] **Step 5: Wire sandbox into prepareAgent**

Edit `cmd/lcoder/main.go`. Add the `sandbox` import to the import block:

```go
	"github.com/lcoder/lcoder/pkg/sandbox"
```

Replace the line `registry := tools.NewRegistry(cwd)` (line 146) with:

```go
	sb, err := sandbox.New(toSandboxConfig(cfg.Sandbox, cwd))
	if err != nil {
		return nil, fmt.Errorf("init sandbox: %w", err)
	}
	registry := tools.NewRegistry(cwd)
	registry.SetSandbox(sb)
```

(`err` is already declared earlier in `prepareAgent`, so use `=` inside the assignment above — i.e. `sb, err := sandbox.New(...)` is fine since `sb` is new. The existing `if err := registry.RegisterBuiltinFactories(cwd); err != nil` line that follows is unchanged.)

- [ ] **Step 6: Write the integration test (zero-regression under default passthrough)**

Create `test/integration/sandbox_integration_test.go`:

```go
package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lcoder/lcoder/pkg/sandbox"
	"github.com/lcoder/lcoder/pkg/tools"
	"github.com/lcoder/lcoder/pkg/tools/builtin"
)

// Default config => empty backend => passthrough. Tools must behave as before.
func TestSandboxPassthroughZeroRegression(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(target, []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}

	sb, err := sandbox.New(sandbox.Config{ProjectRoot: dir}) // empty backend = passthrough
	if err != nil {
		t.Fatalf("sandbox.New: %v", err)
	}
	r := tools.NewRegistry(dir)
	r.SetSandbox(sb)
	r.Register("read", builtin.NewRead(dir))

	res, isErr := r.Execute(context.Background(), "c", "read", map[string]any{"path": "hello.txt"})
	if isErr {
		t.Fatalf("read returned error result: %+v", res)
	}
	if got := res.Content[0].(interface{ String() string }); got != nil {
		// content assertion below uses concrete type
	}
	txt := res.Content[0]
	tc, ok := txt.(interface{ GetText() string })
	_ = tc
	_ = ok
	_ = txt
}
```

Wait — simplify the assertion. Replace the whole function body's result check with a direct content check using the models package:

```go
package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/sandbox"
	"github.com/lcoder/lcoder/pkg/tools"
	"github.com/lcoder/lcoder/pkg/tools/builtin"
)

// Default config => empty backend => passthrough. Tools must behave as before.
func TestSandboxPassthroughZeroRegression(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}

	sb, err := sandbox.New(sandbox.Config{ProjectRoot: dir}) // empty backend = passthrough
	if err != nil {
		t.Fatalf("sandbox.New: %v", err)
	}
	r := tools.NewRegistry(dir)
	r.SetSandbox(sb)
	r.Register("read", builtin.NewRead(dir))

	res, isErr := r.Execute(context.Background(), "c", "read", map[string]any{"path": "hello.txt"})
	if isErr {
		t.Fatalf("read returned error result: %+v", res)
	}
	txt := res.Content[0].(models.TextContent).Text
	if txt != "world" {
		t.Fatalf("read content = %q, want %q", txt, "world")
	}
}
```

(Use only the second, simplified version — delete the first draft above when implementing.)

- [ ] **Step 7: Run the integration test**

Run: `go test ./test/integration/ -run TestSandboxPassthroughZeroRegression -v`
Expected: PASS.

- [ ] **Step 8: Add the commented sandbox section to configs/lcoder.yaml**

Append to `configs/lcoder.yaml`:

```yaml

# Sandbox isolation (optional). Default backend is passthrough (no isolation).
# Set backend to soft-limit to enable best-effort env scrubbing, timeouts,
# output caps, and in-process network/filesystem policy. Not a security boundary.
# sandbox:
#   backend: passthrough        # passthrough | soft-limit | container(stub) | remote(stub)
#   env_allowlist: [PATH, HOME, LANG, SHELL]
#   network:
#     default: deny             # deny | allow
#     allow: ["api.github.com:443"]
#   filesystem:
#     writable: ["."]
#     readable: ["."]
#   limits:
#     max_memory_mb: 512
#     max_cpu_seconds: 60
#     max_output_bytes: 1048576
```

- [ ] **Step 9: Full build + vet + test sweep**

Run: `go build ./... && go vet ./... && go test ./pkg/tools/... ./pkg/config/... ./cmd/lcoder/... ./test/integration/ -count=1`
Expected: PASS (no compile/vet/test failures).

- [ ] **Step 10: Commit**

```bash
git add cmd/lcoder/sandbox.go cmd/lcoder/sandbox_test.go cmd/lcoder/main.go test/integration/sandbox_integration_test.go configs/lcoder.yaml
git commit -m "feat(lcoder): wire sandbox into agent assembly"
```

---

## Final Verification

- [ ] **Build, vet, and test the whole module**

Run: `go build ./... && go vet ./... && go test ./... -count=1`
Expected: PASS. Cross-compile check for the sandbox unix path: `GOOS=linux go build ./...`.

- [ ] **Confirm zero-regression default**

The default config has `Sandbox.Backend == ""` → `sandbox.New` returns passthrough → bash runs via `sh -c` with `os.Environ()`, file Check always allows, http dials directly. No behavior change unless the user opts into `soft-limit`.

---

## Self-Review Notes (spec coverage)

- spec §3 注入机制 → Task 1 (`SandboxAware` + `SetSandbox` + Register probe). 用 `SetSandbox` 代替改 `NewRegistry` 签名以避免破坏 10 个测试调用点; 机制等价。
- spec §4.1 bash → Task 2 (`sb.Exec` + Combined + 超时标记 + 退化分支).
- spec §4.2 file 工具 + helper + find/grep 子路径 Check → Task 3.
- spec §4.3 http DialContext → Task 4.
- spec §5 config → Task 5; §5.2 映射 → Task 6 `toSandboxConfig`; §5.3 lcoder.yaml → Task 6 Step 8.
- spec §6 装配 + ProjectRoot=cwd → Task 6 Step 5.
- spec §7 默认 passthrough 零回归 → Task 6 集成测试 + Final Verification.
- spec §8 测试策略 → 各 Task 的 FakeSandbox/denyFS 测试覆盖 registry/bash/file/http/config/装配.
