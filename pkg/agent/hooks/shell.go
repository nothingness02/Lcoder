// Package hooks provides agent hook implementations.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/compaction"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/models"
)

const defaultShellHookTimeout = 30 * time.Second

// shellHookInput is the JSON payload sent to a shell hook via stdin.
// Matches Kimi Code's hook input convention.
type shellHookInput struct {
	HookEvent    string         `json:"hook_event"`
	ToolName     string         `json:"tool_name,omitempty"`
	ToolInput    map[string]any `json:"tool_input,omitempty"`
	ToolResult   string         `json:"tool_result,omitempty"`
	IsError      bool           `json:"is_error,omitempty"`
	StopReason   string         `json:"stop_reason,omitempty"`
	Conversation string         `json:"conversation,omitempty"`
	SessionID    string         `json:"session_id,omitempty"`
	CWD          string         `json:"cwd,omitempty"`
}

// runShellHook spawns a shell command, pipes JSON input to stdin, and
// interprets the exit code. Process-tree kill ensures the entire command
// tree (shell + grandchildren) is terminated on timeout or abort.
//
// Exit-code semantics (matching Kimi Code):
//
//	0 — allow
//	2 — block (stderr is the reason)
//	other — allow
//
// Fail-open: timeout and abort signal result in "allow", never error,
// so a wedged hook cannot block the agent.
func runShellHook(ctx context.Context, cfg config.ShellHookConfig, input shellHookInput) (*agent.BeforeToolCallResult, error) {
	_, res, err := runShellHookCapture(ctx, cfg, input)
	return res, err
}

// runShellHookCapture is runShellHook plus stdout capture (before_compact
// returns its summary on stdout).
func runShellHookCapture(ctx context.Context, cfg config.ShellHookConfig, input shellHookInput) (string, *agent.BeforeToolCallResult, error) {
	if !cfg.Enabled || cfg.Command == "" {
		return "", nil, nil
	}

	timeout := time.Duration(cfg.Timeout) * time.Second
	if timeout <= 0 {
		timeout = defaultShellHookTimeout
	}

	hookCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(hookCtx, "sh", "-c", cfg.Command)
	setProcGroup(cmd)
	cmd.Stdin = bytes.NewReader(mustMarshal(input))

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	// Kill the whole process tree on the way out so no grandchildren leak.
	killTree(cmd)

	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	// Fail-open: timeout → allow.
	if err != nil && hookCtx.Err() == context.DeadlineExceeded {
		return "", nil, nil
	}

	// Fail-open: abort → allow.
	if err != nil && ctx.Err() != nil {
		return "", nil, nil
	}

	switch exitCode {
	case 0:
		return stdout.String(), nil, nil // allow
	case 2:
		reason := stderr.String()
		if reason == "" {
			reason = "blocked by shell hook"
		}
		return "", &agent.BeforeToolCallResult{Block: true, Reason: reason}, nil
	default:
		// Non-zero exit but not 2: allow. The hook is advisory — a crash or
		// unexpected failure must not block the agent (fail-open).
		if err != nil {
			return "", nil, fmt.Errorf("shell hook exited %d: %s", exitCode, stderr.String())
		}
		return "", nil, nil
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

// ShellOnStop returns a ContinuationDecider that runs a shell command when
// the agent is about to stop (Claude Code Stop hook semantics):
//
//	exit 0 — allow the stop
//	exit 2 — block the stop: the loop continues and stderr is fed back to
//	         the model via the steer callback
//
// A disabled or unconfigured hook passes through as "allow the stop"
// (cont=false), matching the empty-chain default.
func ShellOnStop(cfg config.ShellHookConfig, sessionID string, steer func(reason string)) agent.ContinuationDecider {
	return func(ctx context.Context, stop agent.StopContext) (bool, error) {
		if !cfg.Enabled || cfg.Command == "" {
			return false, nil
		}
		res, err := runShellHook(ctx, cfg, shellHookInput{
			HookEvent:  "on_stop",
			StopReason: string(stop.Reason),
			SessionID:  sessionID,
		})
		if err != nil {
			return false, err
		}
		if res == nil || !res.Block {
			return false, nil // exit 0:允许停止
		}
		if steer != nil {
			steer(res.Reason)
		}
		return true, nil // exit 2:续跑
	}
}

// ShellBeforeCompact wraps the built-in summarizer: when the hook succeeds
// (exit 0, non-empty stdout), stdout replaces the LLM summary. The hook
// receives the serialized conversation (prior summary prepended) on stdin,
// mirroring the extension runtime's session_before_compact payload.
func ShellBeforeCompact(cfg config.ShellHookConfig, sessionID string, fallback contextmgr.SummarizeFunc) contextmgr.SummarizeFunc {
	return func(ctx context.Context, messages []models.AgentMessage, prior string) (string, error) {
		if !cfg.Enabled || cfg.Command == "" {
			return fallback(ctx, messages, prior)
		}
		conversation := compaction.SerializeConversation(messages, 2000)
		if p := strings.TrimSpace(prior); p != "" {
			conversation = "<previous_summary>\n" + p + "\n</previous_summary>\n\n" + conversation
		}
		stdout, _, err := runShellHookCapture(ctx, cfg, shellHookInput{
			HookEvent:    "before_compact",
			Conversation: conversation,
			SessionID:    sessionID,
		})
		if err == nil && strings.TrimSpace(stdout) != "" {
			return strings.TrimSpace(stdout), nil
		}
		return fallback(ctx, messages, prior)
	}
}
