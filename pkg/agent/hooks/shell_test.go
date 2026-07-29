package hooks

import (
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
	_, err := h(context.Background(), info)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}
