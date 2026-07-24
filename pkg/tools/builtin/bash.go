package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/tools"
)

// Bash executes shell commands.
type Bash struct {
	cwd string
}

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

	start := time.Now()
	stdout, stderr, exitCode, timedOut, execErr := runShellCommand(
		ctx, shell, command, cwd, os.Environ(), time.Duration(timeout)*time.Second)
	elapsed := time.Since(start)

	res := buildBashResult(command, cwd, stdout, stderr, exitCode, timedOut, elapsed)
	if execErr != nil {
		return res, fmt.Errorf("command failed: %w", execErr)
	}
	if exitCode != 0 || timedOut {
		// Business-level failure: surface result to the LLM without a Go error.
		return res, nil
	}
	copied, copyErr := b.copyOutputs(outputs, cwd)
	if copyErr != nil {
		return res, fmt.Errorf("command succeeded but copying outputs failed: %w", copyErr)
	}
	if len(copied) > 0 {
		res.Details["outputs_copied"] = copied
	}
	return res, nil
}

// buildBashResult assembles the model-facing result of a finished command.
// stdout/stderr are each capped at maxBashOutputChars (head 75% / tail 25%)
// so a runaway command cannot flood the context; stderr is labeled so the
// model can tell it apart from stdout; non-zero exits and timeouts are
// flagged IsError with the exit code visible in the text. Commands that ran
// for a second or more get a duration prefix so the model does not mistake a
// quiet long-running command for one that was skipped.
func buildBashResult(command, cwd, stdout, stderr string, exitCode int, timedOut bool, elapsed time.Duration) models.ToolExecutionResult {
	stdout, truncatedOut := truncateHeadTail(stdout, maxBashOutputChars)
	stderr, truncatedErr := truncateHeadTail(stderr, maxBashOutputChars)

	var b strings.Builder
	if elapsed >= time.Second {
		fmt.Fprintf(&b, "[command ran for %s]\n", elapsed.Round(time.Millisecond))
	}
	b.WriteString(stdout)
	if stderr != "" {
		if stdout != "" {
			b.WriteString("\n")
		}
		b.WriteString("[stderr]\n")
		b.WriteString(stderr)
	}
	if timedOut {
		b.WriteString("\n[command timed out]")
	}
	if exitCode != 0 {
		fmt.Fprintf(&b, "\n[exit code: %d]", exitCode)
	}

	details := map[string]any{
		"command":   command,
		"cwd":       cwd,
		"stdout":    stdout,
		"stderr":    stderr,
		"exit_code": exitCode,
		"timed_out": timedOut,
	}
	if truncatedOut || truncatedErr {
		details["truncated"] = true
	}

	return models.ToolExecutionResult{
		Content: []models.ContentPart{models.TextContent{Text: strings.TrimSpace(b.String())}},
		Details: details,
		IsError: exitCode != 0 || timedOut,
	}
}

// truncateHeadTail caps s at max runes, keeping the first 75% and the last
// 25% with an explicit marker stating how much was elided. It operates on
// runes to avoid splitting multi-byte characters.
func truncateHeadTail(s string, max int) (string, bool) {
	runes := []rune(s)
	if len(runes) <= max {
		return s, false
	}
	head := max * 3 / 4
	tail := max - head
	elided := len(runes) - head - tail
	return string(runes[:head]) +
		fmt.Sprintf("\n[... truncated %d chars ...]\n", elided) +
		string(runes[len(runes)-tail:]), true
}

// copyOutputs copies declared output files back into the workspace after a
// successful command.
func (b *Bash) copyOutputs(outputs []string, cwd string) ([]string, error) {
	var copied []string
	for _, out := range outputs {
		src := out
		if !filepath.IsAbs(src) {
			src = filepath.Join(cwd, src)
		}
		src = filepath.Clean(src)
		dst := resolveInCwd(cwd, out)
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

var _ tools.Executable = (*Bash)(nil)
