package main

import (
	"github.com/lcoder/lcoder/internal/paths"
	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/agent/hooks"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/llm/catalog"
	"github.com/lcoder/lcoder/pkg/llm/engine"
	llmprovider "github.com/lcoder/lcoder/pkg/llm/provider"
	"github.com/lcoder/lcoder/pkg/permissions"
)

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
