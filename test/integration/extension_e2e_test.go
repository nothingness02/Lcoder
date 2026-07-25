//go:build integration

package integration

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/extension/proto"
	extruntime "github.com/lcoder/lcoder/pkg/extension/runtime"
)

// buildHelper compiles the helper extension into a temp binary.
func buildHelper(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "extension-helper")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/extension-helper")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, out)
	}
	return bin
}

func TestExtensionEndToEnd(t *testing.T) {
	bin := buildHelper(t)
	host := extruntime.NewHost(extruntime.HostOptions{Timeout: 5 * time.Second})
	defer host.Close()
	var warns []string
	host.OnWarning = func(m string) { warns = append(warns, m) }

	host.Load([]extruntime.Manifest{{Name: "helper", Command: []string{bin}}})
	if !host.HasHook(proto.HookToolCall) || !host.HasHook(proto.HookInput) {
		t.Fatalf("helper hooks not registered; warns=%v", warns)
	}

	// tool_call: danger blocked, others allowed.
	res := host.RunToolCallHooks(context.Background(), "danger", nil)
	if !res.Block || res.Reason != "danger is blocked" {
		t.Fatalf("res %+v", res)
	}
	if res := host.RunToolCallHooks(context.Background(), "bash", nil); res.Block {
		t.Fatal("bash must be allowed")
	}

	// input transform.
	if got := host.RunInputHook(context.Background(), "hi"); got.Text != "hi!" {
		t.Fatalf("input %+v", got)
	}

	// command.
	out, err := host.InvokeCommand(context.Background(), "ping", "")
	if err != nil || out != "pong" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestExtensionCrashIsolation(t *testing.T) {
	host := extruntime.NewHost(extruntime.HostOptions{Timeout: time.Second})
	defer host.Close()
	var warns []string
	host.OnWarning = func(m string) { warns = append(warns, m) }
	// A nonexistent binary: spawn fails, warning recorded, host unaffected.
	host.Load([]extruntime.Manifest{{Name: "crasher", Command: []string{filepath.Join(t.TempDir(), "does-not-exist")}}})
	if len(warns) == 0 {
		t.Fatal("expected a warning for the crashed extension")
	}
	if res := host.RunToolCallHooks(context.Background(), "bash", nil); res.Block {
		t.Fatal("dead extension must not block tools")
	}
}
