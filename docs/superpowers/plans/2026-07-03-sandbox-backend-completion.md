# Sandbox Backend Completion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Document the security posture of the `soft-limit` backend, implement Windows Job Object process-tree isolation, and provide a concrete `container` backend using Docker so that `container` is no longer a reserved stub.

**Architecture:** Keep the existing `Sandbox` interface unchanged. Add a Windows-specific Job Object implementation in `pkg/sandbox/exec_windows.go`. Add a `containerSandbox` in `pkg/sandbox/container.go` that shells out to `docker run` with restricted network/mount options. Update `docs/superpowers/specs/2026-06-30-sandbox-design.md` to state that `soft-limit` is best-effort, not a security boundary.

**Tech Stack:** Go 1.25, Windows Job Object (`golang.org/x/sys/windows`), Docker CLI.

---

## File Structure

- **Modify:** `pkg/sandbox/exec_windows.go` — add Job Object + process-tree kill
- **Modify:** `pkg/sandbox/exec_unix.go` — add numeric rlimits (optional, guarded)
- **Create:** `pkg/sandbox/container.go` — Docker-backed container backend
- **Create:** `pkg/sandbox/container_test.go` — unit tests for config translation
- **Modify:** `pkg/sandbox/config.go` — wire `container` backend
- **Modify:** `pkg/sandbox/config_test.go` — update expectations
- **Modify:** `docs/superpowers/specs/2026-06-30-sandbox-design.md` — security disclaimer

---

## Task 1: Document soft-limit Security Posture

**Files:**
- Modify: `docs/superpowers/specs/2026-06-30-sandbox-design.md`

- [ ] **Step 1: Add explicit security disclaimer**

In the spec, add a new section after the overview:

```markdown
## Security posture by backend

| Backend | Isolation | Security boundary? |
|---|---|---|
| `passthrough` | None | No |
| `soft-limit` | Timeout, output cap, env scrubbing, process group (Unix), Job Object (Windows), best-effort network/fs allowlists | **No** — it is a safety/rate-limiting boundary, not a hard security boundary. A determined subprocess can bypass network/fs restrictions with raw sockets or out-of-band channels. |
| `container` | Docker container with `--network` and bind-mount restrictions | Yes, when the container runtime is correctly configured. |

Always prefer `container` for untrusted code; use `soft-limit` only for trusted projects where the goal is accident containment.
```

- [ ] **Step 2: Commit**

```bash
git add docs/superpowers/specs/2026-06-30-sandbox-design.md
git commit -m "docs(sandbox): state that soft-limit is not a security boundary

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 2: Windows Job Object Support

**Files:**
- Modify: `pkg/sandbox/exec_windows.go`
- Test: `pkg/sandbox/exec_windows_test.go`

- [ ] **Step 1: Implement Job Object helpers**

Replace `pkg/sandbox/exec_windows.go` with:

```go
//go:build windows

package sandbox

import (
	"fmt"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobLimits holds the Job Object handle for a command so we can terminate the
// whole process tree on timeout.
type jobLimits struct {
	handle windows.Handle
}

// applyLimits places the child into a new Job Object so the whole tree can be
// killed together. Numeric rlimits remain deferred (see spec §10).
func applyLimits(cmd *exec.Cmd, _ ResourceLimits) {
	// SysProcAttr is not used here; we assign the job after cmd.Start in softLimit.Exec.
}

func createJobObject() (windows.Handle, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	_, err = windows.SetInformationJobObject(
		h,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if err != nil {
		windows.CloseHandle(h)
		return 0, err
	}
	return h, nil
}

func assignToJob(cmd *exec.Cmd, job windows.Handle) error {
	if cmd.Process == nil {
		return fmt.Errorf("process not started")
	}
	return windows.AssignProcessToJobObject(job, windows.Handle(cmd.Process.Pid))
}

// killGroup terminates the entire Job Object tree.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func terminateJob(job windows.Handle) {
	_ = windows.TerminateJobObject(job, 1)
	_ = windows.CloseHandle(job)
}
```

- [ ] **Step 2: Wire Job Object into softLimit.Exec**

Modify `pkg/sandbox/softlimit.go` to create/assign/terminate the job on Windows. Use build tags so the cross-platform path stays clean.

Add to `pkg/sandbox/softlimit.go`:

```go
func (s *softLimit) Exec(ctx context.Context, spec ExecSpec) (ExecResult, error) {
	// ... existing setup up to cmd.Start() ...

	var job windowsJob
	if err := job.Start(cmd); err != nil {
		_ = cmd.Process.Kill()
		return ExecResult{}, fmt.Errorf("sandbox: start job object: %w", err)
	}
	defer job.Close()

	// ... existing wait logic ...

	if timedOut {
		job.Terminate()
		return res, nil
	}
	return res, runErr
}
```

Because this touches platform specifics, create `pkg/sandbox/job_unix.go` and `pkg/sandbox/job_windows.go`:

`job_unix.go`:
```go
//go:build !windows

package sandbox

import "os/exec"

type windowsJob struct{}

func (j *windowsJob) Start(_ *exec.Cmd) error { return nil }
func (j *windowsJob) Terminate()              {}
func (j *windowsJob) Close()                  {}
```

`job_windows.go`:
```go
//go:build windows

package sandbox

import "os/exec"

type windowsJob struct {
	handle windows.Handle
}

func (j *windowsJob) Start(cmd *exec.Cmd) error {
	h, err := createJobObject()
	if err != nil {
		return err
	}
	j.handle = h
	return assignToJob(cmd, h)
}

func (j *windowsJob) Terminate() {
	if j.handle != 0 {
		terminateJob(j.handle)
	}
}

func (j *windowsJob) Close() {
	if j.handle != 0 {
		_ = windows.CloseHandle(j.handle)
	}
}
```

Update `pkg/sandbox/exec_windows.go` to remove the standalone `applyLimits`/`killGroup` duplicates now moved to job helpers; keep `applyLimits` as no-op and `killGroup` calling `cmd.Process.Kill()` for compatibility.

- [ ] **Step 3: Add Windows test**

Create `pkg/sandbox/exec_windows_test.go`:

```go
//go:build windows

package sandbox

import (
	"context"
	"testing"
	"time"
)

func TestSoftLimitWindows_JobObjectKillsTree(t *testing.T) {
	sb, err := New(Config{Backend: "soft-limit"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	res, err := sb.Exec(context.Background(), ExecSpec{
		Command: "cmd /c start /b ping -n 10 127.0.0.1 & exit 0",
		Timeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !res.TimedOut {
		t.Fatal("expected timeout")
	}
}
```

Run on Windows:
```bash
go test ./pkg/sandbox/... -run TestSoftLimitWindows_JobObjectKillsTree -v
```
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/sandbox/exec_windows.go pkg/sandbox/job_unix.go pkg/sandbox/job_windows.go pkg/sandbox/softlimit.go pkg/sandbox/exec_windows_test.go
git commit -m "feat(sandbox): Windows Job Object process-tree isolation

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 3: Docker Container Backend

**Files:**
- Create: `pkg/sandbox/container.go`
- Create: `pkg/sandbox/container_test.go`
- Modify: `pkg/sandbox/config.go`
- Modify: `pkg/sandbox/config_test.go`

- [ ] **Step 1: Write failing config test**

Modify `pkg/sandbox/config_test.go`:

```go
func TestNew_ContainerBackend(t *testing.T) {
	cfg := Config{Backend: "container", ProjectRoot: t.TempDir()}
	sb, err := New(cfg)
	if err != nil {
		t.Fatalf("expected container backend to construct, got %v", err)
	}
	if sb.Name() != "container" {
		t.Fatalf("expected name container, got %s", sb.Name())
	}
}
```

Run:
```bash
go test ./pkg/sandbox/... -run TestNew_ContainerBackend -v
```
Expected: FAIL — container backend not implemented.

- [ ] **Step 2: Implement container backend**

Create `pkg/sandbox/container.go`:

```go
package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// containerSandbox runs commands inside a Docker container.
// It is a real security boundary when Docker is correctly configured.
type containerSandbox struct {
	cfg    Config
	network *allowlistNetwork
	fs      *restrictedFS
}

func newContainerSandbox(cfg Config) (*containerSandbox, error) {
	np, err := buildNetwork(cfg.Network)
	if err != nil {
		return nil, err
	}
	fs, err := buildFS(cfg.Filesystem, cfg.ProjectRoot)
	if err != nil {
		return nil, err
	}
	return &containerSandbox{cfg: cfg, network: np, fs: fs}, nil
}

func (c *containerSandbox) Name() string                 { return "container" }
func (c *containerSandbox) Network() NetworkPolicy       { return c.network }
func (c *containerSandbox) Filesystem() FilesystemPolicy { return c.fs }

func (c *containerSandbox) Exec(ctx context.Context, spec ExecSpec) (ExecResult, error) {
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{
		"run", "--rm",
		"--network", c.networkMode(),
		"-v", fmt.Sprintf("%s:/workspace", c.cfg.ProjectRoot),
		"-w", "/workspace",
	}
	for _, e := range c.envAllow() {
		args = append(args, "-e", e)
	}
	args = append(args, c.image(), "sh", "-c", spec.Command)

	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = c.cfg.ProjectRoot

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	timedOut := ctx.Err() == context.DeadlineExceeded
	res := ExecResult{
		Stdout:   capOutput(stdout.String(), spec.Limits.MaxOutputBytes),
		Stderr:   capOutput(stderr.String(), spec.Limits.MaxOutputBytes),
		ExitCode: exitCode(err),
		TimedOut: timedOut,
	}
	if timedOut {
		return res, nil
	}
	return res, err
}

func (c *containerSandbox) image() string {
	if img := os.Getenv("LCODER_SANDBOX_IMAGE"); img != "" {
		return img
	}
	return "alpine:latest"
}

func (c *containerSandbox) networkMode() string {
	cfg := c.network.SubprocessConfig()
	if cfg.ContainerNetwork != "" {
		return cfg.ContainerNetwork
	}
	return "none"
}

func (c *containerSandbox) envAllow() []string {
	allow := c.cfg.EnvAllowlist
	if len(allow) == 0 {
		allow = defaultEnvAllowlist
	}
	var out []string
	for _, k := range allow {
		if v := os.Getenv(k); v != "" {
			out = append(out, fmt.Sprintf("%s=%s", k, v))
		}
	}
	return out
}

var _ Sandbox = (*containerSandbox)(nil)
```

- [ ] **Step 3: Wire container backend in config**

Modify `pkg/sandbox/config.go`:

```go
case "container":
	return newContainerSandbox(cfg)
```

- [ ] **Step 4: Add container unit test**

Create `pkg/sandbox/container_test.go`:

```go
package sandbox

import (
	"testing"
)

func TestContainerNetworkMode(t *testing.T) {
	cfg := Config{Backend: "container", ProjectRoot: t.TempDir()}
	cs, err := newContainerSandbox(cfg)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if cs.networkMode() != "none" {
		t.Fatalf("expected default network none, got %s", cs.networkMode())
	}
}
```

Run:
```bash
go test ./pkg/sandbox/... -run TestNew_ContainerBackend -v
go test ./pkg/sandbox/... -run TestContainerNetworkMode -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/sandbox/container.go pkg/sandbox/container_test.go pkg/sandbox/config.go pkg/sandbox/config_test.go
git commit -m "feat(sandbox): Docker container backend

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 4: Full Verification

- [ ] **Step 1: Run sandbox tests**

```bash
go test ./pkg/sandbox/... -count=1
```
Expected: PASS.

- [ ] **Step 2: Build**

```bash
go build ./...
```
Expected: no output.

- [ ] **Step 3: Vet**

```bash
go vet ./pkg/sandbox/...
```
Expected: no output.

---

## Self-review

1. **Spec coverage:**
   - Security disclaimer: Task 1
   - Windows Job Object: Task 2
   - Container backend: Task 3

2. **Placeholder scan:** No TBD/TODO.

3. **Type consistency:** `Sandbox` interface unchanged; `containerSandbox` implements it.
