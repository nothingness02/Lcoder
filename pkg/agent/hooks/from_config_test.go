package hooks

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/models"
)

func TestFromConfig_ShellHookEnabled(t *testing.T) {
	cfg := config.HookConfig{
		BeforeToolCall: config.ShellHookConfig{
			Enabled: true,
			Command: "exit 2",
		},
	}
	h := FromConfig(cfg, "test-session")
	info := agent.ToolCallInfo{
		ToolCall: models.ToolCallContent{Name: "write", ID: "1"},
		Args:     map[string]any{"path": "main.go"},
	}
	result, err := h(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.Block {
		t.Fatal("expected block from exit-2 shell hook")
	}
}

func TestFromConfig_ShellHookDisabled(t *testing.T) {
	cfg := config.HookConfig{}
	h := FromConfig(cfg, "test-session")
	info := agent.ToolCallInfo{
		ToolCall: models.ToolCallContent{Name: "read", ID: "1"},
		Args:     map[string]any{"path": "main.go"},
	}
	result, err := h(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatal("expected nil when no hooks configured")
	}
}
