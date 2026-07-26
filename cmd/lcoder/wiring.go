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
// connection. Provider entries are resolved fail-fast via the catalog: an
// undeclared route is inferred (logged), an invalid base URL aborts startup.
// The returned engine is passed to llm.NewClient.
func buildEngine(cfg config.Config) (*engine.Engine, error) {
	cachePath := paths.LCoderHome("cache", "models.json")
	cat := catalog.New(catalog.Options{
		Refresh:   true,
		CachePath: cachePath,
		SourceURL: cfg.ModelsSource,
		Overrides: catalogOverridesFromConfig(cfg),
	})
	eng := engine.New(cat)
	for name, conn := range cfg.Providers {
		res, err := cat.ResolveProvider(name, conn.Route, conn.BaseURL)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", name, err)
		}
		if res.Guessed {
			fmt.Fprintf(os.Stderr, "info: provider %q 未声明 route,推断为 %s\n", name, res.Route)
		}
		eng.RegisterProvider(name, llmprovider.Conn{
			BaseURL: res.BaseURL,
			APIKey:  conn.APIKey,
			Route:   res.Route,
			Headers: conn.Headers,
		})
	}
	return eng, nil
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

// stdinTrustPrompter asks the user whether to load a project-level extension.
// It runs before the TUI starts, so plain stdin/stderr prompting is safe.
func stdinTrustPrompter(name, dir string) bool {
	fmt.Fprintf(os.Stderr, "\nProject extension %q (%s) wants to load.\nProject extensions can run arbitrary code. Load it? (y/N): ", name, dir)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	s := strings.ToLower(strings.TrimSpace(line))
	return s == "y" || s == "yes"
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
