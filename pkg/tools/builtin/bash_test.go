package builtin

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lcoder/lcoder/pkg/sandbox"
)

func TestBash_CopiesOutputsOnSuccess(t *testing.T) {
	dir := t.TempDir()
	bash := NewBash(dir).(*Bash)
	fsb := sandbox.NewFakeSandbox()
	fsb.Result = sandbox.ExecResult{Stdout: "ok", ExitCode: 0}
	bash.UseSandbox(fsb)

	// Simulate the sandbox command writing an output file.
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte("# report"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := bash.Execute(context.Background(), "call_1", map[string]any{
		"command": "echo ok",
		"outputs": []any{"report.md"},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	copied, _ := res.Details["outputs_copied"].([]string)
	if len(copied) != 1 || copied[0] != filepath.Join(dir, "report.md") {
		t.Fatalf("outputs_copied = %v, want one entry", copied)
	}
}

func TestBash_DoesNotCopyOutputsOnFailure(t *testing.T) {
	dir := t.TempDir()
	bash := NewBash(dir).(*Bash)
	fsb := sandbox.NewFakeSandbox()
	fsb.Result = sandbox.ExecResult{Stdout: "fail", Stderr: "", ExitCode: 1}
	bash.UseSandbox(fsb)

	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte("# report"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := bash.Execute(context.Background(), "call_1", map[string]any{
		"command": "exit 1",
		"outputs": []any{"report.md"},
	})
	if err != nil {
		t.Fatalf("expected no error for non-zero exit, got %v", err)
	}
	if exitCode, _ := res.Details["exit_code"].(int); exitCode != 1 {
		t.Fatalf("exit_code = %v, want 1", res.Details["exit_code"])
	}
	if _, ok := res.Details["outputs_copied"]; ok {
		t.Fatal("outputs should not be copied on failure")
	}
}
