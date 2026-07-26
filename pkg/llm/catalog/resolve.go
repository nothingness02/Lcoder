// pkg/llm/catalog/resolve.go
package catalog

import (
	"fmt"
	"strings"

	"github.com/lcoder/lcoder/pkg/llm/provider"
)

// ResolvedProvider is the outcome of resolving one configured provider
// connection: which wire it speaks and which endpoint its key is sent to.
type ResolvedProvider struct {
	Route   string // "anthropic" | "openai-responses" | "openai"
	BaseURL string
	Guessed bool // Route was inferred, not declared
}

// ResolveProvider decides the wire and endpoint for one provider entry
// (mirrors kimi-code resolveCatalogImport, cut down to three wires). An
// explicit route passes through; a missing route is inferred from models.dev
// metadata (anthropic/claude → anthropic, codex → openai-responses, else
// openai). A missing base URL falls back to the catalog api, then built-in
// defaults; a blank or placeholder base URL is rejected — silently sending
// the key to the wrong host is a credential leak.
func (c *Catalog) ResolveProvider(name, connRoute, connBaseURL string) (ResolvedProvider, error) {
	route := connRoute
	guessed := false
	if route == "" {
		route = c.inferRoute(name)
		guessed = true
	}
	base, err := c.resolveBase(name, route, connBaseURL)
	if err != nil {
		return ResolvedProvider{}, err
	}
	return ResolvedProvider{Route: route, BaseURL: base, Guessed: guessed}, nil
}

func (c *Catalog) inferRoute(name string) string {
	lower := strings.ToLower(name)
	npm := ""
	if meta, ok := c.ProviderMeta(name); ok {
		npm = strings.ToLower(meta.Npm)
	}
	if strings.Contains(npm, "anthropic") || strings.Contains(lower, "anthropic") || strings.Contains(lower, "claude") {
		return "anthropic"
	}
	if strings.Contains(npm, "codex") || strings.Contains(lower, "codex") {
		return "openai-responses"
	}
	return "openai"
}

func (c *Catalog) resolveBase(name, route, explicit string) (string, error) {
	if explicit != "" {
		b := strings.TrimSpace(explicit)
		if b == "" {
			return "", fmt.Errorf("provider %q: base_url is blank", name)
		}
		if strings.Contains(b, "${") {
			return "", fmt.Errorf("provider %q: base_url contains env placeholder, which config cannot express", name)
		}
		return b, nil
	}
	if meta, ok := c.ProviderMeta(name); ok && meta.API != "" && !strings.Contains(meta.API, "${") {
		return meta.API, nil
	}
	if d := provider.DefaultBaseURL(name); d != "" {
		return d, nil
	}
	// 协议族兜底:openai-responses 与 openai 同 base
	if route == "openai-responses" {
		return provider.DefaultBaseURL("openai"), nil
	}
	if d := provider.DefaultBaseURL(route); d != "" {
		return d, nil
	}
	return "", fmt.Errorf("provider %q: no base URL known; set base_url explicitly", name)
}
