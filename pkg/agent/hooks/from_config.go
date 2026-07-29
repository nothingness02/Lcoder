package hooks

import (
	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/config"
)

// FromConfig builds a composite BeforeToolCallHook from declarative config.
func FromConfig(cfg config.HookConfig, sessionID string) agent.BeforeToolCallHook {
	var hooks []agent.BeforeToolCallHook

	if cfg.BeforeToolCall.Enabled && cfg.BeforeToolCall.Command != "" {
		hooks = append(hooks, ShellBeforeToolCall(cfg.BeforeToolCall, sessionID))
	}

	return CompositeBeforeToolCall(hooks...)
}

// AfterToolCallFromConfig builds an AfterToolCallHook from config.
func AfterToolCallFromConfig(cfg config.HookConfig, sessionID string) agent.AfterToolCallHook {
	if cfg.AfterToolResult.Enabled && cfg.AfterToolResult.Command != "" {
		return ShellAfterToolResult(cfg.AfterToolResult, sessionID)
	}
	return nil
}
