package hooks

import (
	"path/filepath"
	"context"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/models"
)

func TestShellBeforeToolCall_Allow(t *testing.T) {
	cfg := config.ShellHookConfig{
		Enabled: true,
		Command: "exit 0",
		Timeout: 5,
	}
	h := ShellBeforeToolCall(cfg, "sess-1")
	info := agent.ToolCallInfo{
		ToolCall: models.ToolCallContent{Name: "write", ID: "1"},
		Args:     map[string]any{"path": "main.go"},
	}
	result, err := h(context.Background(), info)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil result (allow), got block: %+v", result)
	}
}

func TestShellBeforeToolCall_Block(t *testing.T) {
	cfg := config.ShellHookConfig{
		Enabled: true,
		Command: "echo 'sensitive file blocked' >&2; exit 2",
		Timeout: 5,
	}
	h := ShellBeforeToolCall(cfg, "sess-1")
	info := agent.ToolCallInfo{
		ToolCall: models.ToolCallContent{Name: "write", ID: "1"},
		Args:     map[string]any{"path": ".env"},
	}
	result, err := h(context.Background(), info)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || !result.Block {
		t.Fatal("expected block")
	}
	if !strings.Contains(result.Reason, "sensitive file blocked") {
		t.Fatalf("expected reason to contain 'sensitive file blocked', got %q", result.Reason)
	}
}

func TestShellBeforeToolCall_Disabled(t *testing.T) {
	cfg := config.ShellHookConfig{
		Enabled: false,
		Command: "exit 2",
	}
	h := ShellBeforeToolCall(cfg, "sess-1")
	info := agent.ToolCallInfo{
		ToolCall: models.ToolCallContent{Name: "write", ID: "1"},
		Args:     map[string]any{"path": "main.go"},
	}
	result, err := h(context.Background(), info)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatal("expected nil when disabled")
	}
}

func TestShellBeforeToolCall_EmptyCommand(t *testing.T) {
	cfg := config.ShellHookConfig{
		Enabled: true,
		Command: "",
	}
	h := ShellBeforeToolCall(cfg, "sess-1")
	info := agent.ToolCallInfo{
		ToolCall: models.ToolCallContent{Name: "write", ID: "1"},
		Args:     map[string]any{"path": "main.go"},
	}
	result, err := h(context.Background(), info)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatal("expected nil for empty command")
	}
}

func TestShellBeforeToolCall_Timeout(t *testing.T) {
	cfg := config.ShellHookConfig{
		Enabled: true,
		Command: "sleep 10",
		Timeout: 1,
	}
	h := ShellBeforeToolCall(cfg, "sess-1")
	info := agent.ToolCallInfo{
		ToolCall: models.ToolCallContent{Name: "write", ID: "1"},
		Args:     map[string]any{"path": "main.go"},
	}
	// Fail-open: timeout returns nil (allow), not error.
	result, err := h(context.Background(), info)
	if err != nil {
		t.Fatalf("timeout must fail-open (no error), got: %v", err)
	}
	if result != nil {
		t.Fatalf("timeout must fail-open (no block), got: %+v", result)
	}
}

// exit 2 = 阻止停止(续跑),stderr 经 steer 回调反馈;exit 0 = 允许停止。
func TestShellOnStopExitSemantics(t *testing.T) {
	mark := filepath.Join(t.TempDir(), "mark")
	cfg := config.ShellHookConfig{
		Enabled: true,
		Command: "if [ -f \"" + mark + "\" ]; then exit 0; else touch \"" + mark + "\"; echo 'keep going' >&2; exit 2; fi",
		Timeout: 5,
	}

	var steered []string
	decider := ShellOnStop(cfg, "sess", func(reason string) { steered = append(steered, reason) })

	cont, err := decider(context.Background(), agent.StopContext{})
	if err != nil || !cont {
		t.Fatalf("exit 2 must continue, got cont=%v err=%v", cont, err)
	}
	if len(steered) != 1 || !strings.Contains(steered[0], "keep going") {
		t.Fatalf("stderr must be steered to the model, got %v", steered)
	}

	cont, err = decider(context.Background(), agent.StopContext{})
	if err != nil || cont {
		t.Fatalf("exit 0 must stop, got cont=%v err=%v", cont, err)
	}
}

// 未启用/未配置时直通:允许停止(空链语义)。
func TestShellOnStopDisabled(t *testing.T) {
	decider := ShellOnStop(config.ShellHookConfig{}, "sess", nil)
	cont, err := decider(context.Background(), agent.StopContext{})
	if err != nil || cont {
		t.Fatalf("disabled hook must stop (pass-through), got cont=%v err=%v", cont, err)
	}
}
