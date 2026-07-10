package builtin

import (
	"bytes"
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

// Bash executes shell commands.
type Bash struct {
	cwd string
	sb  sandbox.Sandbox
}

// UseSandbox injects the sandbox used to run commands.
func (b *Bash) UseSandbox(sb sandbox.Sandbox) { b.sb = sb }

// NewBash creates a bash tool.
func NewBash(cwd string) tools.Executable {
	return &Bash{cwd: cwd}
}

func (b *Bash) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name:        "bash",
		Description: "Execute a shell command in the project working directory.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "Shell command to execute",
				},
				"timeout": map[string]any{
					"type":        "integer",
					"description": "Timeout in seconds (default 120)",
				},
				"outputs": map[string]any{
					"type":        "array",
					"description": "Optional list of output file paths to copy back to the workspace on success.",
					"items":       map[string]any{"type": "string"},
				},
			},
			"required": []string{"command"},
		},
		ExecutionMode: models.ExecutionSequential,
	}
}

func (b *Bash) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	command := tools.String(args, "command", "")
	if command == "" {
		return models.ToolExecutionResult{}, fmt.Errorf("missing command")
	}
	outputs := tools.StringSlice(args, "outputs")

	timeout := tools.Int(args, "timeout", 120)

	cwd := b.cwd
	if !filepath.IsAbs(cwd) {
		abs, err := filepath.Abs(cwd)
		if err == nil {
			cwd = abs
		}
	}

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "sh"
	}

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
		res := models.ToolExecutionResult{
			Content: []models.ContentPart{models.TextContent{Text: strings.TrimSpace(output)}},
			Details: map[string]any{
				"command":   command,
				"cwd":       cwd,
				"stdout":    result.Stdout,
				"stderr":    result.Stderr,
				"exit_code": result.ExitCode,
				"timed_out": result.TimedOut,
			},
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
		copied, copyErr := copyOutputs(outputs, cwd, cwd)
		if copyErr != nil {
			return res, fmt.Errorf("command succeeded but copying outputs failed: %w", copyErr)
		}
		if len(copied) > 0 {
			res.Details["outputs_copied"] = copied
		}
		return res, nil
	}

	cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, shell, "-c", command)
	cmd.Dir = cwd
	cmd.Env = os.Environ()

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run()
	stdout := stdoutBuf.String()
	stderr := stderrBuf.String()
	output := mergeOutput(stdout, stderr)

	timedOut := cmdCtx.Err() == context.DeadlineExceeded
	if timedOut {
		output += "\n[command timed out]"
	}

	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	res := models.ToolExecutionResult{
		Content: []models.ContentPart{models.TextContent{Text: strings.TrimSpace(output)}},
		Details: map[string]any{
			"command":   command,
			"cwd":       cwd,
			"stdout":    stdout,
			"stderr":    stderr,
			"exit_code": exitCode,
			"timed_out": timedOut,
		},
	}
	if err != nil {
		return res, fmt.Errorf("command failed: %w", err)
	}
	copied, copyErr := copyOutputs(outputs, cwd, cwd)
	if copyErr != nil {
		return res, fmt.Errorf("command succeeded but copying outputs failed: %w", copyErr)
	}
	if len(copied) > 0 {
		res.Details["outputs_copied"] = copied
	}
	return res, nil
}

func copyOutputs(outputs []string, srcDir, dstDir string) ([]string, error) {
	var copied []string
	for _, out := range outputs {
		src := out
		if !filepath.IsAbs(src) {
			src = filepath.Join(srcDir, src)
		}
		dst := out
		if !filepath.IsAbs(dst) {
			dst = filepath.Join(dstDir, dst)
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return copied, fmt.Errorf("read output %s: %w", out, err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return copied, fmt.Errorf("mkdir for %s: %w", dst, err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return copied, fmt.Errorf("write output %s: %w", dst, err)
		}
		copied = append(copied, dst)
	}
	return copied, nil
}

func mergeOutput(stdout, stderr string) string {
	switch {
	case stderr == "":
		return stdout
	case stdout == "":
		return stderr
	default:
		return stdout + "\n" + stderr
	}
}

var _ tools.Executable = (*Bash)(nil)
