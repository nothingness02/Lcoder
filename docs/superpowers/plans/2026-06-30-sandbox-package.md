# Sandbox Package (`pkg/sandbox`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a self-contained `pkg/sandbox` package that abstracts a `Sandbox` interface with two working backends (`Passthrough`, `SoftLimit`), plus a `FakeSandbox` for consumer tests — isolating command execution and network access behind a swappable interface.

**Architecture:** A `Sandbox` interface exposes three capabilities — `Exec` (subprocess plane), `Network()` and `Filesystem()` (two policies that serve both the in-process plane via `DialContext`/`Check` and the subprocess plane via config/mounts). `Passthrough` is a no-op equivalent to today's bash behavior; `SoftLimit` adds env scrubbing, timeout, output capping, process-group orphan cleanup (Unix), and in-process network/filesystem allowlists. `Container`/`Remote` are reserved (the factory returns an explicit "not implemented" error). Cross-platform process control lives in build-tagged files.

**Tech Stack:** Go 1.25.4, stdlib only (`os/exec`, `syscall`, `net`, `path/filepath`). Module path `github.com/lcoder/lcoder`. No new dependencies.

**Scope note:** This plan delivers the package in isolation. Wiring it into `builtin.Bash`, the `http` tool, file tools, and `lcoder.yaml` config touches the `tools.Factory` signature and is a separate follow-up plan (Plan 2). The reference spec is `docs/superpowers/specs/2026-06-30-sandbox-design.md`.

**File structure:**
- `pkg/sandbox/sandbox.go` — interface + core types (`ExecSpec`, `ExecResult`, `ResourceLimits`, `FSOp`, `Mount`, `SubprocessNetConfig`)
- `pkg/sandbox/env.go` — env allowlist scrubbing with Windows case-folding
- `pkg/sandbox/network.go` — `NetworkPolicy`: passthrough + allowlist dialer
- `pkg/sandbox/filesystem.go` — `FilesystemPolicy`: allow-all + restricted with path-traversal defense
- `pkg/sandbox/passthrough.go` — `Passthrough` backend
- `pkg/sandbox/softlimit.go` — `SoftLimit` backend (platform-agnostic parts)
- `pkg/sandbox/exec_unix.go` / `exec_windows.go` — build-tagged process control
- `pkg/sandbox/config.go` — `Config` + `New()` factory
- `pkg/sandbox/fake.go` — `FakeSandbox`
- matching `*_test.go` files

---

### Task 1: Core types and interface

**Files:**
- Create: `pkg/sandbox/sandbox.go`
- Test: `pkg/sandbox/sandbox_test.go`

- [ ] **Step 1: Write the failing test**

```go
// pkg/sandbox/sandbox_test.go
package sandbox

import "testing"

func TestExecResultCombined(t *testing.T) {
	cases := []struct {
		name           string
		stdout, stderr string
		want           string
	}{
		{"both", "out", "err", "out\nerr"},
		{"stdout only", "out", "", "out"},
		{"stderr only", "", "err", "err"},
		{"neither", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := ExecResult{Stdout: c.stdout, Stderr: c.stderr}
			if got := r.Combined(); got != c.want {
				t.Fatalf("Combined() = %q, want %q", got, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/sandbox/ -run TestExecResultCombined -v`
Expected: FAIL — build error, `ExecResult`/`Combined` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
// pkg/sandbox/sandbox.go

// Package sandbox abstracts isolation of sensitive environment interactions
// (command execution and network access) behind a swappable Sandbox interface.
// Backends range from a no-op Passthrough to an OS-level SoftLimit to reserved
// Container/Remote backends. See docs/superpowers/specs/2026-06-30-sandbox-design.md.
package sandbox

import (
	"context"
	"net"
	"time"
)

// Sandbox isolates sensitive environment interactions. Implementations are
// selected via New and consumed by tools without knowledge of the backend.
type Sandbox interface {
	// Exec runs a command under the backend's isolation policy (subprocess plane).
	Exec(ctx context.Context, spec ExecSpec) (ExecResult, error)
	// Network serves the in-process plane (DialContext) and the subprocess plane
	// (SubprocessConfig).
	Network() NetworkPolicy
	// Filesystem serves the in-process plane (Check) and the subprocess plane
	// (SubprocessMounts).
	Filesystem() FilesystemPolicy
	// Name identifies the backend for logging and telemetry.
	Name() string
}

// ExecSpec describes one controlled command execution.
type ExecSpec struct {
	Command string        // command line passed to "sh -c"
	Cwd     string        // working directory
	Env     []string      // KEY=VALUE entries; backend may filter to an allowlist
	Timeout time.Duration // 0 means the backend default (60s)
	Limits  ResourceLimits
}

// ResourceLimits bounds a sandboxed process. Enforcement is backend- and
// platform-dependent; unsupported limits degrade explicitly (see spec §10).
type ResourceLimits struct {
	MaxMemoryMB    int
	MaxCPUSeconds  int
	MaxOutputBytes int
}

// ExecResult separates stdout and stderr so callers can distinguish normal
// output from error streams. Combined reproduces a merged-output contract.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	TimedOut bool
}

// Combined merges stdout and stderr, joining with a newline only when both are
// non-empty.
func (r ExecResult) Combined() string {
	switch {
	case r.Stderr == "":
		return r.Stdout
	case r.Stdout == "":
		return r.Stderr
	default:
		return r.Stdout + "\n" + r.Stderr
	}
}

// FSOp is a filesystem access mode checked by FilesystemPolicy.
type FSOp int

const (
	FSRead FSOp = iota
	FSWrite
)

// Mount describes a subprocess-plane filesystem binding for container backends.
type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// SubprocessNetConfig carries subprocess-plane network wiring. Empty fields mean
// the backend does not constrain subprocess network (e.g. Passthrough).
type SubprocessNetConfig struct {
	ProxyEnv         []string // KEY=VALUE proxy hints (best-effort, bypassable)
	ContainerNetwork string   // --network value for container backends
}

// NetworkPolicy decides reachability for both planes.
type NetworkPolicy interface {
	// DialContext is injected into in-process consumers (http/MCP). Truly enforced.
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
	// SubprocessConfig returns subprocess-plane wiring derived from this policy.
	SubprocessConfig() SubprocessNetConfig
}

// FilesystemPolicy decides file access for both planes.
type FilesystemPolicy interface {
	// Check is called by in-process file tools before access. Truly enforced.
	Check(path string, op FSOp) error
	// SubprocessMounts returns subprocess-plane mounts for container backends.
	SubprocessMounts() []Mount
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/sandbox/ -run TestExecResultCombined -v`
Expected: PASS (4 subtests).

- [ ] **Step 5: Commit**

```bash
git add pkg/sandbox/sandbox.go pkg/sandbox/sandbox_test.go
git commit -m "feat(sandbox): core Sandbox interface and types"
```

---

### Task 2: Environment scrubbing with Windows case-folding

**Files:**
- Create: `pkg/sandbox/env.go`
- Test: `pkg/sandbox/env_test.go`

- [ ] **Step 1: Write the failing test**

```go
// pkg/sandbox/env_test.go
package sandbox

import (
	"slices"
	"testing"
)

func TestScrubEnvAllowlist(t *testing.T) {
	env := []string{"PATH=/usr/bin", "AWS_SECRET=top", "HOME=/home/u", "no_equals_entry"}
	got := scrubEnvFold(env, []string{"PATH", "HOME"}, false)
	want := []string{"PATH=/usr/bin", "HOME=/home/u"}
	if !slices.Equal(got, want) {
		t.Fatalf("scrubEnvFold = %v, want %v", got, want)
	}
}

func TestScrubEnvCaseFold(t *testing.T) {
	// Windows stores "Path"; folding must keep it when allowlist has "PATH".
	env := []string{"Path=C:\\Windows", "SECRET_TOKEN=x"}

	folded := scrubEnvFold(env, []string{"PATH"}, true)
	if !slices.Equal(folded, []string{"Path=C:\\Windows"}) {
		t.Fatalf("folded keep Path: got %v", folded)
	}

	strict := scrubEnvFold(env, []string{"PATH"}, false)
	if len(strict) != 0 {
		t.Fatalf("strict should drop Path (case-sensitive miss): got %v", strict)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/sandbox/ -run TestScrubEnv -v`
Expected: FAIL — `scrubEnvFold` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
// pkg/sandbox/env.go
package sandbox

import (
	"runtime"
	"strings"
)

// scrubEnv filters env (KEY=VALUE entries) to the allowlist using OS-appropriate
// name matching. Credential-bearing vars are excluded by virtue of the allowlist
// (default-deny). On Windows env names are case-insensitive, so matching folds case.
func scrubEnv(env, allowlist []string) []string {
	return scrubEnvFold(env, allowlist, runtime.GOOS == "windows")
}

// scrubEnvFold is the testable core; fold controls case-insensitive name matching.
func scrubEnvFold(env, allowlist []string, fold bool) []string {
	allow := make(map[string]bool, len(allowlist))
	for _, name := range allowlist {
		allow[foldName(name, fold)] = true
	}
	var out []string
	for _, kv := range env {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		if allow[foldName(kv[:eq], fold)] {
			out = append(out, kv)
		}
	}
	return out
}

func foldName(name string, fold bool) string {
	if fold {
		return strings.ToUpper(name)
	}
	return name
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/sandbox/ -run TestScrubEnv -v`
Expected: PASS (2 tests).

- [ ] **Step 5: Commit**

```bash
git add pkg/sandbox/env.go pkg/sandbox/env_test.go
git commit -m "feat(sandbox): env allowlist scrubbing with Windows case-folding"
```

---

### Task 3: NetworkPolicy — passthrough and allowlist dialer

**Files:**
- Create: `pkg/sandbox/network.go`
- Test: `pkg/sandbox/network_test.go`

- [ ] **Step 1: Write the failing test**

```go
// pkg/sandbox/network_test.go
package sandbox

import (
	"context"
	"net"
	"strings"
	"testing"
)

func TestHostMatches(t *testing.T) {
	cases := []struct {
		pattern, host string
		want          bool
	}{
		{"api.example.com", "api.example.com", true},
		{"api.example.com", "other.com", false},
		{"*.github.com", "api.github.com", true},
		{"*.github.com", "github.com", false}, // wildcard requires a subdomain
		{"*.github.com", "evilgithub.com", false},
	}
	for _, c := range cases {
		if got := hostMatches(c.pattern, c.host); got != c.want {
			t.Fatalf("hostMatches(%q,%q)=%v want %v", c.pattern, c.host, got, c.want)
		}
	}
}

func TestAllowlistDenies(t *testing.T) {
	p := &allowlistNetwork{
		defaultAllow: false,
		entries:      []allowEntry{{host: "allowed.com", port: 443}},
		dialer:       &net.Dialer{},
	}
	_, err := p.DialContext(context.Background(), "tcp", "blocked.com:443")
	if err == nil || !strings.Contains(err.Error(), "denied by policy") {
		t.Fatalf("expected deny error, got %v", err)
	}
}

func TestAllowlistAllowsAndDials(t *testing.T) {
	// Spin up a local listener and allow it; DialContext should connect.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	var port int
	_, _ = fmtSscan(portStr, &port)

	p := &allowlistNetwork{
		defaultAllow: false,
		entries:      []allowEntry{{host: "127.0.0.1", port: port}},
		dialer:       &net.Dialer{},
	}
	conn, err := p.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("expected dial success, got %v", err)
	}
	conn.Close()
}

func TestPassthroughNetworkSubprocessConfigEmpty(t *testing.T) {
	p := &passthroughNetwork{dialer: &net.Dialer{}}
	if cfg := p.SubprocessConfig(); len(cfg.ProxyEnv) != 0 || cfg.ContainerNetwork != "" {
		t.Fatalf("expected empty subprocess config, got %+v", cfg)
	}
}
```

Add this tiny helper at the bottom of the test file (avoids importing strconv just for the test):

```go
func fmtSscan(s string, p *int) (int, error) {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	*p = n
	return 1, nil
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/sandbox/ -run 'TestHostMatches|TestAllowlist|TestPassthroughNetwork' -v`
Expected: FAIL — `allowlistNetwork`, `passthroughNetwork`, `hostMatches`, `allowEntry` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
// pkg/sandbox/network.go
package sandbox

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// passthroughNetwork allows all traffic via a plain dialer.
type passthroughNetwork struct{ dialer *net.Dialer }

func (p *passthroughNetwork) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return p.dialer.DialContext(ctx, network, addr)
}

func (p *passthroughNetwork) SubprocessConfig() SubprocessNetConfig { return SubprocessNetConfig{} }

// allowEntry is one host:port allow rule. host may be a "*.example.com" wildcard;
// port 0 means any port.
type allowEntry struct {
	host string
	port int
}

// allowlistNetwork enforces an allowlist for in-process dials.
type allowlistNetwork struct {
	defaultAllow bool
	entries      []allowEntry
	dialer       *net.Dialer
}

func (p *allowlistNetwork) allowed(host string, port int) bool {
	for _, e := range p.entries {
		if e.port != 0 && e.port != port {
			continue
		}
		if hostMatches(e.host, host) {
			return true
		}
	}
	return p.defaultAllow
}

// hostMatches reports whether host matches pattern. A "*." prefix matches any
// strict subdomain (not the bare apex).
func hostMatches(pattern, host string) bool {
	host = strings.ToLower(host)
	pattern = strings.ToLower(pattern)
	if pattern == host {
		return true
	}
	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[1:] // ".example.com"
		return strings.HasSuffix(host, suffix) && len(host) > len(suffix)
	}
	return false
}

func (p *allowlistNetwork) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("sandbox: bad address %q: %w", addr, err)
	}
	port, _ := strconv.Atoi(portStr)
	if !p.allowed(host, port) {
		return nil, fmt.Errorf("sandbox: network access to %s denied by policy", addr)
	}
	return p.dialer.DialContext(ctx, network, addr)
}

// SubprocessConfig returns empty wiring; subprocess proxy hints are deferred to a
// later iteration (see spec §6 — best-effort, bypassable).
func (p *allowlistNetwork) SubprocessConfig() SubprocessNetConfig { return SubprocessNetConfig{} }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/sandbox/ -run 'TestHostMatches|TestAllowlist|TestPassthroughNetwork' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/sandbox/network.go pkg/sandbox/network_test.go
git commit -m "feat(sandbox): NetworkPolicy with passthrough and allowlist dialer"
```

---

### Task 4: FilesystemPolicy — restricted with path-traversal defense

**Files:**
- Create: `pkg/sandbox/filesystem.go`
- Test: `pkg/sandbox/filesystem_test.go`

- [ ] **Step 1: Write the failing test**

```go
// pkg/sandbox/filesystem_test.go
package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func newRestrictedFS(t *testing.T, root string) *restrictedFS {
	t.Helper()
	real, err := resolvePath(root)
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	return &restrictedFS{readable: []string{real}, writable: []string{real}}
}

func TestRestrictedFSAllowsInsideRoot(t *testing.T) {
	root := t.TempDir()
	fs := newRestrictedFS(t, root)
	if err := fs.Check(filepath.Join(root, "sub", "file.txt"), FSWrite); err != nil {
		t.Fatalf("expected allow inside root, got %v", err)
	}
}

func TestRestrictedFSDeniesTraversal(t *testing.T) {
	root := t.TempDir()
	fs := newRestrictedFS(t, root)
	escape := filepath.Join(root, "..", "..", "etc", "passwd")
	if err := fs.Check(escape, FSRead); err == nil {
		t.Fatal("expected traversal to be denied")
	}
}

func TestRestrictedFSDeniesSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is restricted on Windows CI")
	}
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	fs := newRestrictedFS(t, root)
	// link/secret.txt resolves into `outside`, which is outside the root.
	if err := fs.Check(filepath.Join(link, "secret.txt"), FSRead); err == nil {
		t.Fatal("expected symlink escape to be denied")
	}
}

func TestRestrictedFSPrefixIsSegmentAligned(t *testing.T) {
	// A sibling dir sharing a name prefix must not match (e.g. /p vs /pevil).
	parent := t.TempDir()
	root := filepath.Join(parent, "proj")
	sibling := filepath.Join(parent, "projevil")
	_ = os.MkdirAll(root, 0o755)
	_ = os.MkdirAll(sibling, 0o755)
	fs := newRestrictedFS(t, root)
	if err := fs.Check(filepath.Join(sibling, "x.txt"), FSRead); err == nil {
		t.Fatal("expected sibling prefix to be denied")
	}
}

func TestAllowAllFS(t *testing.T) {
	var fs allowAllFS
	if err := fs.Check("/anything", FSWrite); err != nil {
		t.Fatalf("allowAllFS should permit, got %v", err)
	}
	if fs.SubprocessMounts() != nil {
		t.Fatal("expected nil mounts")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/sandbox/ -run 'TestRestrictedFS|TestAllowAllFS' -v`
Expected: FAIL — `restrictedFS`, `allowAllFS`, `resolvePath` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
// pkg/sandbox/filesystem.go
package sandbox

import (
	"fmt"
	"path/filepath"
	"strings"
)

// allowAllFS permits every path (Passthrough backend).
type allowAllFS struct{}

func (allowAllFS) Check(string, FSOp) error { return nil }
func (allowAllFS) SubprocessMounts() []Mount { return nil }

// restrictedFS confines in-process file access to configured roots. Roots must
// already be normalized via resolvePath at construction time.
type restrictedFS struct {
	readable []string
	writable []string
}

func (f *restrictedFS) Check(path string, op FSOp) error {
	real, err := resolvePath(path)
	if err != nil {
		return fmt.Errorf("sandbox: cannot resolve %q: %w", path, err)
	}
	roots := f.readable
	label := "read"
	if op == FSWrite {
		roots = f.writable
		label = "write"
	}
	if !withinAny(real, roots) {
		return fmt.Errorf("sandbox: %s access to %q denied by policy", label, path)
	}
	return nil
}

func (f *restrictedFS) SubprocessMounts() []Mount { return nil }

// resolvePath normalizes a path to its real physical form (separator-folded) for
// prefix matching: Abs -> Clean -> EvalSymlinks. For a not-yet-existing target
// (e.g. a write destination) it resolves the parent dir then re-attaches the
// basename, so a symlinked parent cannot escape the root.
func resolvePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return normalizeSep(real), nil
	}
	parent := filepath.Dir(abs)
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return normalizeSep(abs), nil // parent absent too; best we can do
	}
	return normalizeSep(filepath.Join(realParent, filepath.Base(abs))), nil
}

// normalizeSep folds OS separators to "/" so matching is separator-agnostic
// (matters on Windows where "\\" and "/" both appear).
func normalizeSep(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}

// withinAny reports whether target equals or is nested under any root. Prefix
// checks are segment-aligned so "/projevil" does not match root "/proj".
func withinAny(target string, roots []string) bool {
	for _, root := range roots {
		if target == root {
			return true
		}
		prefix := root
		if !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		if strings.HasPrefix(target, prefix) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/sandbox/ -run 'TestRestrictedFS|TestAllowAllFS' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/sandbox/filesystem.go pkg/sandbox/filesystem_test.go
git commit -m "feat(sandbox): FilesystemPolicy with path-traversal defense"
```

---

### Task 5: Passthrough backend

**Files:**
- Create: `pkg/sandbox/passthrough.go`
- Test: `pkg/sandbox/passthrough_test.go`

- [ ] **Step 1: Write the failing test**

```go
// pkg/sandbox/passthrough_test.go
package sandbox

import (
	"context"
	"strings"
	"testing"
)

func TestPassthroughExecRunsCommand(t *testing.T) {
	sb := &passthrough{network: &passthroughNetwork{dialer: nil}}
	res, err := sb.Exec(context.Background(), ExecSpec{Command: "go version", Cwd: "."})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !strings.HasPrefix(res.Stdout, "go version") {
		t.Fatalf("expected go version on stdout, got %q / stderr %q", res.Stdout, res.Stderr)
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d", res.ExitCode)
	}
}

func TestPassthroughExecCapturesExitCode(t *testing.T) {
	sb := &passthrough{network: &passthroughNetwork{dialer: nil}}
	res, _ := sb.Exec(context.Background(), ExecSpec{Command: "exit 3", Cwd: "."})
	if res.ExitCode != 3 {
		t.Fatalf("expected exit 3, got %d", res.ExitCode)
	}
}

func TestPassthroughMetadata(t *testing.T) {
	sb := &passthrough{network: &passthroughNetwork{dialer: nil}}
	if sb.Name() != "passthrough" {
		t.Fatalf("name = %q", sb.Name())
	}
	if err := sb.Filesystem().Check("/anything", FSWrite); err != nil {
		t.Fatalf("passthrough fs should allow, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/sandbox/ -run TestPassthrough -v`
Expected: FAIL — `passthrough` type / `exitCode` / `shellArgs` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
// pkg/sandbox/passthrough.go
package sandbox

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"time"
)

// passthrough performs no isolation; it mirrors the historical bash behavior
// (sh -c, inherited environment) and an allow-all filesystem policy.
type passthrough struct {
	network *passthroughNetwork
}

func (p *passthrough) Name() string                 { return "passthrough" }
func (p *passthrough) Network() NetworkPolicy        { return p.network }
func (p *passthrough) Filesystem() FilesystemPolicy  { return allowAllFS{} }

func (p *passthrough) Exec(ctx context.Context, spec ExecSpec) (ExecResult, error) {
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	name, args := shellArgs(spec.Command)
	cmd := exec.CommandContext(runCtx, name, args...)
	cmd.Dir = spec.Cwd
	env := spec.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	timedOut := runCtx.Err() == context.DeadlineExceeded
	res := ExecResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode(err),
		TimedOut: timedOut,
	}
	if timedOut {
		return res, nil
	}
	return res, err
}

// shellArgs resolves the shell invocation, honoring $SHELL with an "sh" fallback.
func shellArgs(command string) (string, []string) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "sh"
	}
	return shell, []string{"-c", command}
}

// exitCode extracts a process exit code from a run error (-1 for non-exit errors).
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return -1
}

var _ Sandbox = (*passthrough)(nil)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/sandbox/ -run TestPassthrough -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/sandbox/passthrough.go pkg/sandbox/passthrough_test.go
git commit -m "feat(sandbox): Passthrough backend mirroring current bash behavior"
```

---

### Task 6: SoftLimit backend with orphan-safe process control

**Files:**
- Create: `pkg/sandbox/softlimit.go`
- Create: `pkg/sandbox/exec_unix.go`
- Create: `pkg/sandbox/exec_windows.go`
- Test: `pkg/sandbox/softlimit_test.go`
- Test: `pkg/sandbox/exec_unix_test.go`

- [ ] **Step 1: Write the failing test**

```go
// pkg/sandbox/softlimit_test.go
package sandbox

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func newSoftLimit() *softLimit {
	return &softLimit{
		network:  &allowlistNetwork{dialer: &net.Dialer{}},
		fs:       &restrictedFS{},
		envAllow: []string{"PATH", "HOME", "SHELL", "LANG", "SystemRoot", "ComSpec"},
	}
}

func TestSoftLimitScrubsEnv(t *testing.T) {
	sb := newSoftLimit()
	// SECRET is not in the allowlist, so the child must not see it.
	res, err := sb.Exec(context.Background(), ExecSpec{
		Command: `echo "[$SECRET]"`,
		Cwd:     ".",
		Env:     []string{"PATH=" + pathEnv(), "SECRET=topsecret"},
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if strings.Contains(res.Stdout, "topsecret") {
		t.Fatalf("secret leaked into child env: %q", res.Stdout)
	}
}

func TestSoftLimitTimeout(t *testing.T) {
	sb := newSoftLimit()
	start := time.Now()
	res, _ := sb.Exec(context.Background(), ExecSpec{
		Command: "sleep 30",
		Cwd:     ".",
		Env:     []string{"PATH=" + pathEnv()},
		Timeout: 200 * time.Millisecond,
	})
	if !res.TimedOut {
		t.Fatal("expected TimedOut=true")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout did not kill promptly")
	}
}

func TestSoftLimitOutputCap(t *testing.T) {
	sb := newSoftLimit()
	res, err := sb.Exec(context.Background(), ExecSpec{
		Command: `printf 'aaaaaaaaaa'`, // 10 bytes
		Cwd:     ".",
		Env:     []string{"PATH=" + pathEnv()},
		Limits:  ResourceLimits{MaxOutputBytes: 4},
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !strings.HasPrefix(res.Stdout, "aaaa") || !strings.Contains(res.Stdout, "truncated") {
		t.Fatalf("expected truncated output, got %q", res.Stdout)
	}
}

func TestSoftLimitMetadata(t *testing.T) {
	sb := newSoftLimit()
	if sb.Name() != "soft-limit" {
		t.Fatalf("name = %q", sb.Name())
	}
}
```

Add a small env helper at the bottom of `softlimit_test.go` so the child can find the shell/coreutils:

```go
import "os"

func pathEnv() string { return os.Getenv("PATH") }
```

(Place the `import "os"` in the file's import block rather than inline.)

And the Unix-only orphan test:

```go
// pkg/sandbox/exec_unix_test.go
//go:build !windows

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A backgrounded grandchild must be killed when the parent times out, otherwise
// it would create the sentinel after we return. Process-group kill prevents this.
func TestSoftLimitKillsOrphans(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "SENTINEL")
	sb := newSoftLimit()
	_, _ = sb.Exec(context.Background(), ExecSpec{
		Command: "(sleep 1 && touch " + sentinel + ") & echo started",
		Cwd:     dir,
		Env:     []string{"PATH=" + pathEnv()},
		Timeout: 150 * time.Millisecond,
	})
	// Wait past the grandchild's sleep; if the group was killed it never fires.
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatal("orphan grandchild survived: sentinel was created")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/sandbox/ -run TestSoftLimit -v`
Expected: FAIL — `softLimit`, `applyLimits`, `killGroup`, `capOutput` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
// pkg/sandbox/softlimit.go
package sandbox

import (
	"bytes"
	"context"
	"os/exec"
	"time"
)

// softLimit applies best-effort isolation with no external dependencies: env
// scrubbing, timeout, output capping, and (on Unix) process-group orphan
// cleanup. It enforces the in-process network/filesystem policies truly, but the
// subprocess plane is best-effort only — it is NOT a security boundary (spec §4).
type softLimit struct {
	network  *allowlistNetwork
	fs       *restrictedFS
	envAllow []string
}

func (s *softLimit) Name() string                { return "soft-limit" }
func (s *softLimit) Network() NetworkPolicy       { return s.network }
func (s *softLimit) Filesystem() FilesystemPolicy { return s.fs }

func (s *softLimit) Exec(ctx context.Context, spec ExecSpec) (ExecResult, error) {
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	name, args := shellArgs(spec.Command)
	// We manage the kill ourselves (not CommandContext) so we can signal the
	// whole process group and reap orphaned grandchildren.
	cmd := exec.Command(name, args...)
	cmd.Dir = spec.Cwd
	cmd.Env = scrubEnv(spec.Env, s.envAllow)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	applyLimits(cmd, spec.Limits) // platform-specific (Setpgid on Unix; no-op on Windows)

	if err := cmd.Start(); err != nil {
		return ExecResult{}, err
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var runErr error
	timedOut := false
	select {
	case <-runCtx.Done():
		timedOut = runCtx.Err() == context.DeadlineExceeded
		killGroup(cmd)
		<-done // reap
	case runErr = <-done:
	}

	res := ExecResult{
		Stdout:   capOutput(stdout.String(), spec.Limits.MaxOutputBytes),
		Stderr:   capOutput(stderr.String(), spec.Limits.MaxOutputBytes),
		ExitCode: exitCode(runErr),
		TimedOut: timedOut,
	}
	if timedOut {
		return res, nil
	}
	return res, runErr
}

// capOutput truncates s to max bytes (0 = unlimited), appending a marker.
func capOutput(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "\n[output truncated]"
}

var _ Sandbox = (*softLimit)(nil)
```

```go
// pkg/sandbox/exec_unix.go
//go:build !windows

package sandbox

import (
	"os/exec"
	"syscall"
)

// applyLimits places the child in its own process group so the whole tree can be
// signaled on timeout. Numeric rlimits (RLIMIT_AS/RLIMIT_CPU) are intentionally
// deferred — they carry cross-platform and OOM-killer hazards (spec §10) and are
// a follow-up; orphan safety is the guarantee provided here.
func applyLimits(cmd *exec.Cmd, _ ResourceLimits) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup sends SIGKILL to the entire process group (negative PID), reaping
// backgrounded grandchildren that a leader-only kill would orphan.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
```

```go
// pkg/sandbox/exec_windows.go
//go:build windows

package sandbox

import "os/exec"

// applyLimits is a documented degradation on Windows: process-tree (Job Object)
// isolation and numeric rlimits are not yet implemented. Timeout and output
// capping from the cross-platform path still apply (spec §10). Job Object support
// is a follow-up.
func applyLimits(_ *exec.Cmd, _ ResourceLimits) {}

// killGroup terminates the direct process. Backgrounded grandchildren may survive
// until Job Object support lands; this is the explicit Windows degradation.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/sandbox/ -run TestSoftLimit -v`
Expected: PASS. (On Unix, `TestSoftLimitKillsOrphans` also passes; it is skipped from the build on Windows.)

- [ ] **Step 5: Commit**

```bash
git add pkg/sandbox/softlimit.go pkg/sandbox/exec_unix.go pkg/sandbox/exec_windows.go pkg/sandbox/softlimit_test.go pkg/sandbox/exec_unix_test.go
git commit -m "feat(sandbox): SoftLimit backend with orphan-safe process control"
```

---

### Task 7: Config and New() factory

**Files:**
- Create: `pkg/sandbox/config.go`
- Test: `pkg/sandbox/config_test.go`

- [ ] **Step 1: Write the failing test**

```go
// pkg/sandbox/config_test.go
package sandbox

import (
	"strings"
	"testing"
)

func TestNewDefaultsToPassthrough(t *testing.T) {
	sb, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if sb.Name() != "passthrough" {
		t.Fatalf("expected passthrough, got %q", sb.Name())
	}
}

func TestNewSoftLimit(t *testing.T) {
	sb, err := New(Config{
		Backend:     "soft-limit",
		ProjectRoot: t.TempDir(),
		Network:     NetworkConfig{Allow: []string{"api.example.com:443", "*.github.com:443"}},
		Filesystem:  FilesystemConfig{Readable: []string{"."}, Writable: []string{"."}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if sb.Name() != "soft-limit" {
		t.Fatalf("expected soft-limit, got %q", sb.Name())
	}
}

func TestNewReservedBackendsError(t *testing.T) {
	for _, b := range []string{"container", "remote"} {
		_, err := New(Config{Backend: b})
		if err == nil || !strings.Contains(err.Error(), "not yet implemented") {
			t.Fatalf("backend %q: expected not-implemented error, got %v", b, err)
		}
	}
}

func TestNewUnknownBackendError(t *testing.T) {
	_, err := New(Config{Backend: "bogus"})
	if err == nil || !strings.Contains(err.Error(), "unknown sandbox backend") {
		t.Fatalf("expected unknown-backend error, got %v", err)
	}
}

func TestParseAllowEntry(t *testing.T) {
	e, err := parseAllowEntry("api.example.com:443")
	if err != nil || e.host != "api.example.com" || e.port != 443 {
		t.Fatalf("parse host:port: %+v err=%v", e, err)
	}
	bare, err := parseAllowEntry("example.com")
	if err != nil || bare.host != "example.com" || bare.port != 0 {
		t.Fatalf("parse bare host: %+v err=%v", bare, err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/sandbox/ -run 'TestNew|TestParseAllowEntry' -v`
Expected: FAIL — `New`, `Config`, `NetworkConfig`, `FilesystemConfig`, `parseAllowEntry` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
// pkg/sandbox/config.go
package sandbox

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"
)

// Config selects and parameterizes a sandbox backend.
type Config struct {
	Backend      string // "" or "passthrough" | "soft-limit" | "container"/"remote" (reserved)
	EnvAllowlist []string
	Network      NetworkConfig
	Filesystem   FilesystemConfig
	Limits       ResourceLimits
	// ProjectRoot is the base for resolving relative filesystem roots. The CALLER
	// injects the project root here (NOT the process CWD) so the policy does not
	// drift across agents or launch directories (spec §8).
	ProjectRoot string
}

// NetworkConfig describes the network allowlist.
type NetworkConfig struct {
	DefaultAllow bool     // true = allow when no entry matches
	Allow        []string // "host:port" entries; host may be "*.example.com"; port "*"/empty = any
}

// FilesystemConfig describes allowed roots (relative to Config.ProjectRoot).
type FilesystemConfig struct {
	Readable []string
	Writable []string
}

var defaultEnvAllowlist = []string{"PATH", "HOME", "LANG", "SHELL"}

// New constructs a Sandbox for the given config. An empty backend defaults to
// passthrough. container/remote are reserved and return an explicit error.
func New(cfg Config) (Sandbox, error) {
	switch cfg.Backend {
	case "", "passthrough":
		return &passthrough{network: &passthroughNetwork{dialer: &net.Dialer{}}}, nil
	case "soft-limit":
		np, err := buildNetwork(cfg.Network)
		if err != nil {
			return nil, err
		}
		fs, err := buildFS(cfg.Filesystem, cfg.ProjectRoot)
		if err != nil {
			return nil, err
		}
		envAllow := cfg.EnvAllowlist
		if len(envAllow) == 0 {
			envAllow = defaultEnvAllowlist
		}
		return &softLimit{network: np, fs: fs, envAllow: envAllow}, nil
	case "container", "remote":
		return nil, fmt.Errorf("sandbox backend %q not yet implemented (interface reserved)", cfg.Backend)
	default:
		return nil, fmt.Errorf("unknown sandbox backend %q", cfg.Backend)
	}
}

func buildNetwork(c NetworkConfig) (*allowlistNetwork, error) {
	entries := make([]allowEntry, 0, len(c.Allow))
	for _, s := range c.Allow {
		e, err := parseAllowEntry(s)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return &allowlistNetwork{defaultAllow: c.DefaultAllow, entries: entries, dialer: &net.Dialer{}}, nil
}

func buildFS(c FilesystemConfig, root string) (*restrictedFS, error) {
	readable, err := resolveRoots(c.Readable, root)
	if err != nil {
		return nil, err
	}
	writable, err := resolveRoots(c.Writable, root)
	if err != nil {
		return nil, err
	}
	return &restrictedFS{readable: readable, writable: writable}, nil
}

// resolveRoots makes each root absolute against base, then normalizes it to its
// real physical path (so runtime checks use the same canonical form).
func resolveRoots(roots []string, base string) ([]string, error) {
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		if !filepath.IsAbs(r) {
			r = filepath.Join(base, r)
		}
		real, err := resolvePath(r)
		if err != nil {
			return nil, err
		}
		out = append(out, real)
	}
	return out, nil
}

// parseAllowEntry parses "host:port", "host:*", or bare "host" (any port).
func parseAllowEntry(s string) (allowEntry, error) {
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		return allowEntry{host: s, port: 0}, nil // bare host = any port
	}
	port := 0
	if portStr != "" && portStr != "*" {
		p, err := strconv.Atoi(portStr)
		if err != nil {
			return allowEntry{}, fmt.Errorf("sandbox: bad port in allow entry %q: %w", s, err)
		}
		port = p
	}
	return allowEntry{host: host, port: port}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/sandbox/ -run 'TestNew|TestParseAllowEntry' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/sandbox/config.go pkg/sandbox/config_test.go
git commit -m "feat(sandbox): Config and New() backend factory"
```

---

### Task 8: FakeSandbox for consumer tests

**Files:**
- Create: `pkg/sandbox/fake.go`
- Test: `pkg/sandbox/fake_test.go`

- [ ] **Step 1: Write the failing test**

```go
// pkg/sandbox/fake_test.go
package sandbox

import (
	"context"
	"errors"
	"testing"
)

func TestFakeSandboxRecordsAndReturns(t *testing.T) {
	f := NewFakeSandbox()
	f.Result = ExecResult{Stdout: "canned", ExitCode: 0}

	res, err := f.Exec(context.Background(), ExecSpec{Command: "anything", Cwd: "/tmp"})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.Stdout != "canned" {
		t.Fatalf("expected canned result, got %q", res.Stdout)
	}
	if len(f.Calls) != 1 || f.Calls[0].Command != "anything" {
		t.Fatalf("expected recorded call, got %+v", f.Calls)
	}
}

func TestFakeSandboxReturnsErr(t *testing.T) {
	f := NewFakeSandbox()
	f.Err = errors.New("boom")
	_, err := f.Exec(context.Background(), ExecSpec{})
	if err == nil || err.Error() != "boom" {
		t.Fatalf("expected boom, got %v", err)
	}
}

func TestFakeSandboxSatisfiesInterface(t *testing.T) {
	var _ Sandbox = NewFakeSandbox()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/sandbox/ -run TestFakeSandbox -v`
Expected: FAIL — `NewFakeSandbox`/`FakeSandbox` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
// pkg/sandbox/fake.go
package sandbox

import (
	"context"
	"net"
)

// FakeSandbox records Exec calls and returns programmed results. Intended for
// consumer (tool) tests so they never touch real processes or the network.
type FakeSandbox struct {
	Calls     []ExecSpec
	Result    ExecResult
	Err       error
	NetPolicy NetworkPolicy
	FSPolicy  FilesystemPolicy
}

// NewFakeSandbox returns a FakeSandbox with allow-all policies by default.
func NewFakeSandbox() *FakeSandbox {
	return &FakeSandbox{
		NetPolicy: &passthroughNetwork{dialer: &net.Dialer{}},
		FSPolicy:  allowAllFS{},
	}
}

func (f *FakeSandbox) Exec(_ context.Context, spec ExecSpec) (ExecResult, error) {
	f.Calls = append(f.Calls, spec)
	return f.Result, f.Err
}

func (f *FakeSandbox) Network() NetworkPolicy       { return f.NetPolicy }
func (f *FakeSandbox) Filesystem() FilesystemPolicy { return f.FSPolicy }
func (f *FakeSandbox) Name() string                 { return "fake" }

var _ Sandbox = (*FakeSandbox)(nil)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/sandbox/ -v`
Expected: PASS — the entire package test suite is green.

- [ ] **Step 5: Commit**

```bash
git add pkg/sandbox/fake.go pkg/sandbox/fake_test.go
git commit -m "feat(sandbox): FakeSandbox test double"
```

---

## Final verification

- [ ] Run the full package suite and vet:

```bash
go test ./pkg/sandbox/... -v
go vet ./pkg/sandbox/...
```

Expected: all tests PASS, no vet warnings.

## Self-review notes (against the spec)

- **Interface (spec §3):** `Sandbox`/`ExecSpec`/`ExecResult`/`NetworkPolicy`/`FilesystemPolicy` — Tasks 1, 3, 4. `ExecResult` splits stdout/stderr with `Combined()` — Task 1.
- **Backends + matrix (spec §4):** Passthrough — Task 5; SoftLimit (env scrub, timeout, output cap, in-process net/fs) — Tasks 2,3,4,6; Container/Remote reserved error — Task 7.
- **Path traversal (spec §4.1):** Abs→Clean→EvalSymlinks, parent fallback, separator fold, segment-aligned prefix — Task 4 with dedicated tests.
- **Platform hazards (spec §4.2):** env case-folding — Task 2; orphan process-group kill (Unix) + documented Windows degradation — Task 6.
- **Config semantics (spec §8):** `ProjectRoot`-based root resolution (not process CWD) — Task 7.
- **Deferred (own follow-up plan, noted in scope):** integration into Bash/http/file tools + `lcoder.yaml` parsing + setup wiring; numeric rlimits; Windows Job Object; subprocess proxy-env hints. These are intentionally out of this plan and recorded as explicit degradations in the code comments.
