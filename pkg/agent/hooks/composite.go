package hooks

import (
	"context"
	"fmt"

	"github.com/lcoder/lcoder/pkg/agent"
)

// CompositeBeforeToolCall runs multiple hooks in order.
// The first non-nil blocking result wins; errors short-circuit. Non-blocking
// ModifiedArgs are threaded through the chain: each later hook sees the
// rewritten args, and the composite returns the latest modification.
func CompositeBeforeToolCall(hooks ...agent.BeforeToolCallHook) agent.BeforeToolCallHook {
	return func(ctx context.Context, info agent.ToolCallInfo) (*agent.BeforeToolCallResult, error) {
		var mods map[string]any
		for _, h := range hooks {
			if h == nil {
				continue
			}
			if mods != nil {
				info.Args = mods // later hooks see rewritten args
			}
			result, err := h(ctx, info)
			if err != nil {
				return nil, fmt.Errorf("hook error: %w", err)
			}
			if result == nil {
				continue
			}
			if result.Block {
				return result, nil
			}
			if result.ModifiedArgs != nil {
				mods = result.ModifiedArgs
			}
		}
		if mods != nil {
			return &agent.BeforeToolCallResult{ModifiedArgs: mods}, nil
		}
		return nil, nil
	}
}

// CompositeAfterToolCall runs multiple after-tool-call hooks in order.
func CompositeAfterToolCall(hooks ...agent.AfterToolCallHook) agent.AfterToolCallHook {
	return func(ctx context.Context, info agent.ToolCallResultInfo) (*agent.AfterToolCallResult, error) {
		for _, h := range hooks {
			if h == nil {
				continue
			}
			result, err := h(ctx, info)
			if err != nil {
				return nil, fmt.Errorf("hook error: %w", err)
			}
			if result != nil {
				return result, nil
			}
		}
		return nil, nil
	}
}
