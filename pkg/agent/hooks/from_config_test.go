package hooks

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/models"
)

func TestFromConfig(t *testing.T) {
	cfg := config.HookConfig{
		SensitiveFileCheck: config.SensitiveFileCheckHookConfig{
			Enabled:  true,
			Patterns: []string{"*.env"},
		},
	}
	h := FromConfig(cfg, "test-session")
	info := agent.ToolCallInfo{
		ToolCall: models.ToolCallContent{Name: "read", ID: "1"},
		Args:     map[string]any{"path": "secrets.env"},
	}
	result, err := h(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.Block {
		t.Fatal("expected block")
	}
}

func TestFromConfigDisabled(t *testing.T) {
	cfg := config.HookConfig{}
	h := FromConfig(cfg, "test-session")
	if h == nil {
		t.Fatal("expected non-nil hook")
	}
	info := agent.ToolCallInfo{
		ToolCall: models.ToolCallContent{Name: "read", ID: "1"},
		Args:     map[string]any{"path": "foo.txt"},
	}
	result, err := h(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatal("expected nil when disabled")
	}
}
