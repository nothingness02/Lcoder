package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/tools"
	"github.com/lcoder/lcoder/pkg/tools/builtin"
)

// ── Test helpers ──────────────────────────────────────────────────────────

// newPathGuardExecutor builds an executor wired with a real read, write, and
// ls tool, plus a permission engine. The cwd is set to the test temp dir so
// relative-path tests resolve predictably.
func newPathGuardExecutor(t *testing.T) (*executor, string) {
	t.Helper()
	dir := t.TempDir()

	// Create a workspace-relative file so "real" reads succeed.
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Create a subdirectory for ls tests.
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := tools.NewRegistry(".")
	r.Register("read", builtin.NewRead(dir))
	r.Register("write", builtin.NewWrite(dir))
	r.Register("ls", builtin.NewLs(dir))
	r.Register("grep", builtin.NewGrep(dir))
	r.Register("find", builtin.NewFind(dir))

	engine := permissions.NewEngine(permissions.Config{
		Rules: map[string]permissions.RuleTable{
			"read":  {"*": permissions.Allow},
			"write": {"*": permissions.Allow},
			"ls":    {"*": permissions.Allow},
			"grep":  {"*": permissions.Allow},
			"find":  {"*": permissions.Allow},
			"bash":  {"*": permissions.Allow},
			"edit":  {"*": permissions.Allow},
		},
	})

	mm := NewModeManager()
	mm.modes["code"] = ModeConfig{Name: "code"}

	cfg := Config{
		Mode:        "code",
		ModeManager: mm,
		UserConfirm: &stubConfirm{allow: true},
	}

	return &executor{
		cfg:         &cfg,
		mgr:         contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192}),
		registry:    r,
		permissions: engine,
		emitter:     &eventEmitter{bus: events.New()},
		dedup:       make(map[string]models.AgentMessage),
	}, dir
}

// pathGuardToolResult is a shortcut that executes a single-call batch and
// returns the result content + error flag.
func pathGuardToolResult(e *executor, name string, args map[string]any) (string, bool) {
	results, _ := e.execute(context.Background(), 0, models.AgentMessage{}, []models.ToolCallContent{
		{ID: "call_test", Name: name, Arguments: args},
	})
	tr := results[0].Content[0].(models.ToolResultContent)
	return tr.Text(), tr.IsError
}

// ── Read: sensitive-file blocking ─────────────────────────────────────────

func TestPathGuard_ReadDotEnvBlocked(t *testing.T) {
	e, _ := newPathGuardExecutor(t)

	text, isErr := pathGuardToolResult(e, "read", map[string]any{"path": ".env"})
	if !isErr {
		t.Fatal("read .env must be blocked")
	}
	if !strings.Contains(text, "PATH_SENSITIVE") && !strings.Contains(text, "sensitive") {
		t.Fatalf("error should mention sensitive file, got %q", text)
	}
	if !strings.Contains(text, "<system>") {
		t.Fatalf("error should use <system> wrapper, got %q", text)
	}
}

func TestPathGuard_ReadSSHKeyBlocked(t *testing.T) {
	e, _ := newPathGuardExecutor(t)

	for _, key := range []string{"id_rsa", "id_ed25519", "id_ecdsa"} {
		text, isErr := pathGuardToolResult(e, "read", map[string]any{"path": key})
		if !isErr {
			t.Fatalf("read %s must be blocked", key)
		}
		if !strings.Contains(text, "sensitive") {
			t.Fatalf("error for %s should mention sensitive, got %q", key, text)
		}
	}
}

func TestPathGuard_ReadCredentialsBlocked(t *testing.T) {
	e, _ := newPathGuardExecutor(t)

	text, isErr := pathGuardToolResult(e, "read", map[string]any{"path": "credentials"})
	if !isErr {
		t.Fatal("read credentials must be blocked")
	}
	if !strings.Contains(text, "sensitive") {
		t.Fatalf("error should mention sensitive, got %q", text)
	}
}

// ── Write: sensitive-file blocking (must happen BEFORE permission check) ──

func TestPathGuard_WriteDotEnvBlockedBeforePermission(t *testing.T) {
	e, _ := newPathGuardExecutor(t)

	// Even though stubConfirm allows everything and the permission engine has
	// a write rule, the path guard must deny sensitive files first.
	text, isErr := pathGuardToolResult(e, "write", map[string]any{
		"path":    "../.env",
		"content": "SECRET=evil",
	})
	if !isErr {
		t.Fatal("write ../.env must be blocked")
	}
	if !strings.Contains(text, "sensitive") {
		t.Fatalf("error should mention sensitive, got %q", text)
	}
	// Confirm it's NOT a permission-engine message — must be path guard.
	if strings.Contains(text, "no permission rule covers") {
		t.Fatal("error should come from path guard, not permission engine")
	}
}

// ── Read: workspace boundary ──────────────────────────────────────────────

func TestPathGuard_ReadRelativeEscapeBlocked(t *testing.T) {
	e, _ := newPathGuardExecutor(t)

	text, isErr := pathGuardToolResult(e, "read", map[string]any{"path": "../../../etc/passwd"})
	if !isErr {
		t.Fatal("relative path escaping workspace must be blocked")
	}
	if !strings.Contains(text, "outside") && !strings.Contains(text, "absolute") {
		t.Fatalf("error should mention outside/absolute, got %q", text)
	}
	if !strings.Contains(text, "<system>") {
		t.Fatalf("error should use <system> wrapper, got %q", text)
	}
}

func TestPathGuard_ReadDotDotEscapeBlocked(t *testing.T) {
	e, _ := newPathGuardExecutor(t)

	_, isErr := pathGuardToolResult(e, "read", map[string]any{"path": "../secret.txt"})
	if !isErr {
		t.Fatal("read ../secret.txt must be blocked")
	}
}

// ── Read: allowed operations ──────────────────────────────────────────────

func TestPathGuard_ReadNormalFileAllowed(t *testing.T) {
	e, _ := newPathGuardExecutor(t)

	text, isErr := pathGuardToolResult(e, "read", map[string]any{"path": "hello.txt"})
	if isErr {
		t.Fatalf("read hello.txt should succeed, got error: %q", text)
	}
	if !strings.Contains(text, "hello world") {
		t.Fatalf("should read file content, got %q", text)
	}
}

func TestPathGuard_ReadDotEnvExampleAllowed(t *testing.T) {
	e, dir := newPathGuardExecutor(t)

	// Create an exempt file.
	if err := os.WriteFile(filepath.Join(dir, ".env.example"), []byte("KEY=placeholder"), 0o644); err != nil {
		t.Fatal(err)
	}

	text, isErr := pathGuardToolResult(e, "read", map[string]any{"path": ".env.example"})
	if isErr {
		t.Fatalf("read .env.example should be allowed, got: %q", text)
	}
	if !strings.Contains(text, "KEY=placeholder") {
		t.Fatalf("should read file content, got %q", text)
	}
}

func TestPathGuard_LsNormalWorks(t *testing.T) {
	e, _ := newPathGuardExecutor(t)

	text, isErr := pathGuardToolResult(e, "ls", map[string]any{"path": "."})
	if isErr {
		t.Fatalf("ls should succeed, got error: %q", text)
	}
	if !strings.Contains(text, "hello.txt") {
		t.Fatalf("ls should list hello.txt, got %q", text)
	}
}

func TestPathGuard_ReadDotAllowed(t *testing.T) {
	e, _ := newPathGuardExecutor(t)

	// "read ." should fail with "is a directory" (not a path guard error).
	text, isErr := pathGuardToolResult(e, "read", map[string]any{"path": "."})
	if !isErr {
		t.Fatal("read . must fail (is a directory)")
	}
	if strings.Contains(text, "PATH") || strings.Contains(text, "outside") {
		t.Fatalf("read . should fail at the OS level, not the guard: %q", text)
	}
}

// ── Path guard runs before permission engine ──────────────────────────────

func TestPathGuard_BlocksBeforeUserConfirmation(t *testing.T) {
	dir := t.TempDir()
	r := tools.NewRegistry(".")
	r.Register("write", builtin.NewWrite(dir))
	r.Register("read", builtin.NewRead(dir))

	engine := permissions.NewEngine(permissions.Config{
		Rules: map[string]permissions.RuleTable{
			"read":  {"*": permissions.Allow},
			"write": {"*": permissions.Allow},
		},
	})

	// stubConfirm that records calls: if path guard fails first, confirm is
	// never called.
	confirm := &stubConfirm{allow: true}

	mm := NewModeManager()
	mm.modes["code"] = ModeConfig{Name: "code"}

	e := &executor{
		cfg:         &Config{Mode: "code", ModeManager: mm, UserConfirm: confirm},
		mgr:         contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192}),
		registry:    r,
		permissions: engine,
		emitter:     &eventEmitter{bus: events.New()},
		dedup:       make(map[string]models.AgentMessage),
	}

	// Sensitive files: user confirmation must NOT be triggered.
	text, isErr := pathGuardToolResult(e, "write", map[string]any{
		"path":    "../../.env",
		"content": "evil",
	})
	if !isErr {
		t.Fatal("must be blocked")
	}
	if confirm.calls != 0 {
		t.Fatalf("user confirmation must not be triggered; path guard blocks first. "+
			"got %d confirm calls, error text: %q", confirm.calls, text)
	}

	// Relative escape: user confirmation must NOT be triggered.
	text, isErr = pathGuardToolResult(e, "read", map[string]any{"path": "../../../etc/shadow"})
	if !isErr {
		t.Fatal("must be blocked")
	}
	if confirm.calls != 0 {
		t.Fatalf("user confirmation must not be triggered on read escape either. "+
			"got %d confirm calls", confirm.calls)
	}
}

// ── Gretp / Find use OpSearch ─────────────────────────────────────────────

func TestPathGuard_GrepSearchUsesCorrectOperation(t *testing.T) {
	e, _ := newPathGuardExecutor(t)

	text, isErr := pathGuardToolResult(e, "grep", map[string]any{"path": "../outside"})
	if !isErr {
		t.Fatal("must be blocked")
	}
	if !strings.Contains(text, "search") {
		t.Fatalf("grep/find error should mention 'search', got %q", text)
	}
}

func TestPathGuard_FindSearchUsesCorrectOperation(t *testing.T) {
	e, _ := newPathGuardExecutor(t)

	text, isErr := pathGuardToolResult(e, "find", map[string]any{"path": "../outside"})
	if !isErr {
		t.Fatal("must be blocked")
	}
	if !strings.Contains(text, "search") {
		t.Fatalf("find error should mention 'search', got %q", text)
	}
}

// ── No path arg → guard skipped ───────────────────────────────────────────

func TestPathGuard_BashWithoutPathSkipped(t *testing.T) {
	e, _ := newPathGuardExecutor(t)

	// Bash with a command but no "path" arg should NOT trigger the path guard.
	// (bash may still be denied by permission engine — that's fine.)
	text, isErr := pathGuardToolResult(e, "bash", map[string]any{"command": "echo hello"})
	// May pass or fail depending on permission rules, but must NOT be a path
	// guard error.
	if isErr && (strings.Contains(text, "PATH_SENSITIVE") || strings.Contains(text, "PATH_OUTSIDE")) {
		t.Fatalf("bash without path arg must not trigger path guard, got %q", text)
	}
}

// ── todo_write has no path → guard skipped ────────────────────────────────

func TestPathGuard_TodoWriteNoPath(t *testing.T) {
	e, _ := newPathGuardExecutor(t)

	text, isErr := pathGuardToolResult(e, "todo_write", map[string]any{
		"todos": `[{"id":"1","status":"in_progress","content":"test"}]`,
	})
	if isErr && (strings.Contains(text, "PATH_SENSITIVE") || strings.Contains(text, "PATH_OUTSIDE")) {
		t.Fatalf("todo_write must not trigger path guard, got %q", text)
	}
}

// ── Normal write inside workspace still works ─────────────────────────────

func TestPathGuard_WriteInsideWorkspace(t *testing.T) {
	e, _ := newPathGuardExecutor(t)

	text, isErr := pathGuardToolResult(e, "write", map[string]any{
		"path":    "output.txt",
		"content": "generated content",
	})
	if isErr {
		t.Fatalf("write inside workspace should succeed, got: %q", text)
	}
	if !strings.Contains(text, "Wrote") {
		t.Fatalf("write should report success, got %q", text)
	}
}
