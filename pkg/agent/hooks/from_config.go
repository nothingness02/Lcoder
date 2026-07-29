package hooks

import (
	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/config"
)

// FromConfig builds a composite BeforeToolCallHook from declarative config.
// Shell hooks run first (user-facing), then legacy built-in hooks.
func FromConfig(cfg config.HookConfig, sessionID string) agent.BeforeToolCallHook {
	var hooks []agent.BeforeToolCallHook

	// Shell command hook — primary user-facing mechanism.
	if cfg.BeforeToolCall.Enabled && cfg.BeforeToolCall.Command != "" {
		hooks = append(hooks, ShellBeforeToolCall(cfg.BeforeToolCall, sessionID))
	}

	// Legacy built-in hooks — kept for backward compatibility.
	if cfg.SensitiveFileCheck.Enabled && len(cfg.SensitiveFileCheck.Patterns) > 0 {
		hooks = append(hooks, SensitiveFileCheck(cfg.SensitiveFileCheck.Patterns))
	}
	if cfg.BashDenylist.Enabled && len(cfg.BashDenylist.Patterns) > 0 {
		hooks = append(hooks, BashDenylist(cfg.BashDenylist.Patterns))
	}

	return CompositeBeforeToolCall(hooks...)
}

// AfterToolCallFromConfig builds a composite AfterToolCallHook from config.
func AfterToolCallFromConfig(cfg config.HookConfig, sessionID string) agent.AfterToolCallHook {
	if cfg.AfterToolResult.Enabled && cfg.AfterToolResult.Command != "" {
		return ShellAfterToolResult(cfg.AfterToolResult, sessionID)
	}
	return nil
}
