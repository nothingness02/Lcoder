package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/lcoder/lcoder/internal/paths"
	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/agent/hooks"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/llm/catalog"
	"github.com/lcoder/lcoder/pkg/llm/engine"
	llmprovider "github.com/lcoder/lcoder/pkg/llm/provider"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/tui"
)

// buildEngine constructs the in-process LLM engine: a model catalog (snapshot +
// background refresh + models.yaml overrides) plus every configured provider
// connection. The returned engine is passed to llm.NewClient.
func buildEngine(cfg config.Config) *engine.Engine {
	cachePath := paths.LCoderHome("cache", "models.json")
	cat := catalog.New(catalog.Options{
		Refresh:   true,
		CachePath: cachePath,
		Overrides: catalogOverridesFromConfig(cfg),
	})
	eng := engine.New(cat)
	for name, conn := range cfg.Providers {
		eng.RegisterProvider(name, llmprovider.Conn{
			BaseURL: conn.BaseURL,
			APIKey:  conn.APIKey,
			Route:   conn.Route,
			Headers: conn.Headers,
		})
	}
	return eng
}

// catalogOverridesFromConfig maps the user's models.yaml catalog entries into
// explicit catalog overrides so locally-declared models take priority over the
// snapshot/models.dev data.
func catalogOverridesFromConfig(cfg config.Config) []catalog.Entry {
	out := make([]catalog.Entry, 0, len(cfg.Catalog.Models))
	for _, m := range cfg.Catalog.Models {
		out = append(out, catalog.Entry{
			ID:            m.ID,
			Provider:      m.Provider,
			ContextWindow: m.ContextWindow,
			MaxOutput:     m.Budget.MaxOutput,
			Capabilities:  m.Capabilities,
		})
	}
	return out
}

func makeBeforeToolCall(hookCfg config.HookConfig) agent.BeforeToolCallHook {
	return hooks.FromConfig(hookCfg)
}

// cliConfirm reads approval from stdin for CLI runs.
type cliConfirm struct{}

func (c cliConfirm) Confirm(ctx context.Context, info agent.ToolCallInfo) (bool, error) {
	res, err := c.ConfirmWithScope(ctx, info)
	return res.Allow, err
}

func (c cliConfirm) ConfirmWithScope(ctx context.Context, info agent.ToolCallInfo) (agent.ConfirmResult, error) {
	fmt.Fprintf(os.Stderr, "\nPermission request: %s(%s)\n", info.ToolCall.Name, tui.FormatArgsPlain(info.Args))
	ultra := permissions.IsUltraDestructiveCommand(info.BashCommand())
	if ultra {
		fmt.Fprint(os.Stderr, "Destructive command (global allow disabled).\n")
		fmt.Fprint(os.Stderr, "Allow? (y = once / p = project / N = deny): ")
	} else {
		fmt.Fprint(os.Stderr, "Allow? (y = once / p = project / g = global / N = deny): ")
	}

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		// EOF or empty newline counts as denial.
		return agent.ConfirmResult{Allow: false, Scope: agent.ScopeDeny}, nil
	}
	scope, err := parseConfirmScope(strings.TrimSpace(line))
	if err != nil {
		return agent.ConfirmResult{Allow: false, Scope: agent.ScopeDeny}, nil
	}
	if ultra && scope == agent.ScopeGlobal {
		scope = agent.ScopeProject
	}
	return agent.ConfirmResult{Allow: scope != agent.ScopeDeny, Scope: scope}, nil
}

func parseConfirmScope(s string) (agent.ConfirmScope, error) {
	switch strings.ToLower(s) {
	case "y", "yes", "once":
		return agent.ScopeOnce, nil
	case "p", "project":
		return agent.ScopeProject, nil
	case "g", "global":
		return agent.ScopeGlobal, nil
	case "n", "no", "deny", "":
		return agent.ScopeDeny, nil
	default:
		return agent.ScopeDeny, fmt.Errorf("unknown choice")
	}
}

func parsePermissionConfig(pc config.PermissionConfig) []permissions.Rule {
	var rules []permissions.Rule
	for tool, patterns := range pc.Rules {
		for pattern, decision := range patterns {
			rules = append(rules, permissions.Rule{
				Tool:     tool,
				Pattern:  pattern,
				Decision: permissions.Decision(decision),
			})
		}
	}
	return rules
}
