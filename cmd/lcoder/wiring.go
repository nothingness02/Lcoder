package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	cachePath := ""
	if home, err := os.UserHomeDir(); err == nil {
		cachePath = filepath.Join(home, ".lcoder", "cache", "models.json")
	}
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

func (cliConfirm) Confirm(ctx context.Context, info agent.ToolCallInfo) (bool, error) {
	fmt.Fprintf(os.Stderr, "\nPermission request: %s(%s)\nAllow? [y/N] ", info.ToolCall.Name, tui.FormatArgsPlain(info.Args))
	var line string
	if _, err := fmt.Fscanln(os.Stdin, &line); err != nil {
		// EOF or empty newline counts as denial.
		return false, nil
	}
	return strings.EqualFold(strings.TrimSpace(line), "y"), nil
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
