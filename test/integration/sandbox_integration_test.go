package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/sandbox"
	"github.com/lcoder/lcoder/pkg/tools"
	_ "github.com/lcoder/lcoder/pkg/tools/builtin"
)

// resultText extracts the concatenated text parts of a ToolExecutionResult.
func resultText(res models.ToolExecutionResult) string {
	return res.Text()
}

// TestSandboxPassthroughReadNoRegression verifies that with the default
// passthrough backend injected, the file read tool behaves exactly as before:
// the filesystem Check is allow-all, so a normal read succeeds end-to-end.
func TestSandboxPassthroughReadNoRegression(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(fp, []byte("hi there"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	sb, err := sandbox.New(sandbox.Config{Backend: "passthrough", ProjectRoot: dir})
	if err != nil {
		t.Fatalf("sandbox.New: %v", err)
	}

	reg := tools.NewRegistry(dir)
	reg.SetSandbox(sb)
	if err := reg.RegisterBuiltinFactories(dir); err != nil {
		t.Fatalf("register builtins: %v", err)
	}

	res, isErr := reg.Execute(context.Background(), "call-1", "read", map[string]any{"path": fp})
	if isErr {
		t.Fatalf("read reported error: %s", resultText(res))
	}
	if got := resultText(res); got != "hi there" {
		t.Errorf("read content = %q, want %q", got, "hi there")
	}
}

// TestSandboxPassthroughBashNoRegression verifies bash still executes under the
// passthrough backend (best-effort plane), producing expected stdout.
func TestSandboxPassthroughBashNoRegression(t *testing.T) {
	dir := t.TempDir()

	sb, err := sandbox.New(sandbox.Config{Backend: "passthrough", ProjectRoot: dir})
	if err != nil {
		t.Fatalf("sandbox.New: %v", err)
	}

	reg := tools.NewRegistry(dir)
	reg.SetSandbox(sb)
	if err := reg.RegisterBuiltinFactories(dir); err != nil {
		t.Fatalf("register builtins: %v", err)
	}

	res, isErr := reg.Execute(context.Background(), "call-2", "bash", map[string]any{"command": "echo sandbox-ok"})
	if isErr {
		t.Fatalf("bash reported error: %s", resultText(res))
	}
	if got := resultText(res); !strings.Contains(got, "sandbox-ok") {
		t.Errorf("bash output = %q, want it to contain %q", got, "sandbox-ok")
	}
}
