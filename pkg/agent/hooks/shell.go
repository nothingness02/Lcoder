// Package hooks provides agent hook implementations.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/models"
)

const defaultShellHookTimeout = 30 * time.Second

// shellHookInput is the JSON payload sent to a shell hook via stdin.
// Matches Kimi Code's hook input convention.
type shellHookInput struct {
	HookEvent   string         `json:"hook_event"`
	ToolName    string         `json:"tool_name,omitempty"`
	ToolInput   map[string]any `json:"tool_input,omitempty"`
	ToolResult  string         `json:"tool_result,omitempty"`
	IsError     bool           `json:"is_error,omitempty"`
	SessionID   string         `json:"session_id,omitempty"`
	CWD         string         `json:"cwd,omitempty"`
}

// runShellHook spawns a shell command, pipes JSON input to stdin, and
// interprets the exit code.
func runShellHook(ctx context.Context, cfg config.ShellHookConfig, input shellHookInput) (*agent.BeforeToolCallResult, error) {
	if !cfg.Enabled || cfg.Command == "" {
		return nil, nil
	}

	timeout := time.Duration(cfg.Timeout) * time.Second
	if timeout <= 0 {
		timeout = defaultShellHookTimeout
	}

	hookCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(hookCtx, "sh", "-c", cfg.Command)
	cmd.Stdin = bytes.NewReader(mustMarshal(input))

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := cmd.ProcessState.ExitCode()

	if err != nil && hookCtx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("shell hook timed out after %v", timeout)
	}

	switch exitCode {
	case 0:
		return nil, nil // allow
	case 2:
		reason := stderr.String()
		if reason == "" {
			reason = "blocked by shell hook"
		}
		return &agent.BeforeToolCallResult{Block: true, Reason: reason}, nil
	default:
		if err != nil {
			return nil, fmt.Errorf("shell hook exited %d: %s", exitCode, stderr.String())
		}
		return nil, nil // non-zero but not 2 → allow with warning
	}
}

func mustMarshal(v any) []byte {
	data, _ := json.Marshal(v)
	return data
}

// ShellBeforeToolCall returns a BeforeToolCallHook that executes a shell
// command for every tool call. This is the primary user-facing hook.
func ShellBeforeToolCall(cfg config.ShellHookConfig, sessionID string) agent.BeforeToolCallHook {
	return func(ctx context.Context, info agent.ToolCallInfo) (*agent.BeforeToolCallResult, error) {
		return runShellHook(ctx, cfg, shellHookInput{
			HookEvent: "before_tool_call",
			ToolName:  info.ToolCall.Name,
			ToolInput: info.Args,
			SessionID: sessionID,
		})
	}
}

// ShellAfterToolResult returns an AfterToolCallHook that executes a shell
// command after every tool call completes.
func ShellAfterToolResult(cfg config.ShellHookConfig, sessionID string) agent.AfterToolCallHook {
	return func(ctx context.Context, info agent.ToolCallResultInfo) (*agent.AfterToolCallResult, error) {
		_, err := runShellHook(ctx, cfg, shellHookInput{
			HookEvent:  "after_tool_result",
			ToolName:   info.ToolCall.Name,
			ToolInput:  info.Args,
			ToolResult: resultText(info.Result),
			IsError:    info.IsError,
			SessionID:  sessionID,
		})
		return nil, err
	}
}

func resultText(r models.ToolExecutionResult) string {
	var out string
	for _, p := range r.Content {
		if t, ok := p.(models.TextContent); ok {
			out += t.Text
		}
	}
	return out
}
