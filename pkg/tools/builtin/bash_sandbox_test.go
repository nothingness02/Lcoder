package builtin

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/sandbox"
)

func TestBashReturnsStructuredDetails(t *testing.T) {
	b := NewBash("/tmp").(*Bash)
	fake := sandbox.NewFakeSandbox()
	fake.Result = sandbox.ExecResult{
		Stdout:   "hello",
		Stderr:   "warn",
		ExitCode: 42,
	}
	b.UseSandbox(fake)

	res, err := b.Execute(context.Background(), "c1", map[string]any{"command": "echo hello"})
	if err != nil {
		t.Fatalf("unexpected error for non-zero exit: %v", err)
	}

	if got := res.Details["stdout"]; got != "hello" {
		t.Fatalf("stdout detail = %q, want hello", got)
	}
	if got := res.Details["stderr"]; got != "warn" {
		t.Fatalf("stderr detail = %q, want warn", got)
	}
	if got := res.Details["exit_code"]; got != 42 {
		t.Fatalf("exit_code detail = %v, want 42", got)
	}
	if got := res.Details["timed_out"]; got != false {
		t.Fatalf("timed_out detail = %v, want false", got)
	}
}

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

func TestBashSandboxNonZeroExitReturnsBusinessResult(t *testing.T) {
	b := NewBash("/tmp").(*Bash)
	fake := sandbox.NewFakeSandbox()
	fake.Result = sandbox.ExecResult{Stderr: "boom", ExitCode: 1}
	b.UseSandbox(fake)

	res, err := b.Execute(context.Background(), "c1", map[string]any{"command": "false"})
	if err != nil {
		t.Fatalf("expected no system error for non-zero exit: %v", err)
	}
	if res.Details["exit_code"] != 1 {
		t.Fatalf("exit_code = %v, want 1", res.Details["exit_code"])
	}
}

func TestBashSandboxTimeoutMarksOutput(t *testing.T) {
	b := NewBash("/tmp").(*Bash)
	fake := sandbox.NewFakeSandbox()
	fake.Result = sandbox.ExecResult{Stdout: "partial", TimedOut: true}
	b.UseSandbox(fake)

	res, err := b.Execute(context.Background(), "c1", map[string]any{"command": "sleep 99"})
	if err != nil {
		t.Fatalf("expected no system error on timeout: %v", err)
	}
	txt := res.Content[0].(models.TextContent).Text
	if txt != "partial\n[command timed out]" {
		t.Fatalf("output = %q", txt)
	}
	if res.Details["timed_out"] != true {
		t.Fatalf("timed_out = %v, want true", res.Details["timed_out"])
	}
}
