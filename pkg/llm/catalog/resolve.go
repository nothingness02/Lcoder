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
	Route    string // provider name (anthropic | openai | deepseek | ...)
	Protocol provider.Protocol
	BaseURL  string
	Guessed  bool // Route was inferred, not declared
}

// ResolveProvider decides the wire and endpoint for one provider entry
// (mirrors kimi-code resolveCatalogImport, cut down to three wires). An
// explicit connProtocol is validated strictly (unknown values are config
// errors, never silently defaulted); when empty, the protocol is derived from
// the route. A missing route is inferred from models.dev metadata
// (anthropic/claude → anthropic, codex → openai-responses, else openai). A
// missing base URL falls back to the catalog api, then built-in defaults; a
// blank or placeholder base URL is rejected — silently sending the key to the
// wrong host is a credential leak.
func (c *Catalog) ResolveProvider(name, connRoute, connProtocol, connBaseURL string) (ResolvedProvider, error) {
	route := connRoute
	guessed := false
	if route == "" {
		route = c.inferRoute(name)
		guessed = true
	}
	proto := provider.ProtocolForRoute(route)
	if connProtocol != "" {
		p, err := provider.ParseProtocol(connProtocol)
		if err != nil {
			return ResolvedProvider{}, err
		}
		proto = p
	}
	base, err := c.resolveBase(name, route, connBaseURL)
	if err != nil {
		return ResolvedProvider{}, err
	}
	return ResolvedProvider{Route: route, Protocol: proto, BaseURL: base, Guessed: guessed}, nil
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
			return "", fmt.Errorf("base_url is blank")
		}
		if strings.Contains(b, "${") {
			return "", fmt.Errorf("base_url contains env placeholder, which config cannot express")
		}
		return b, nil
	}
	if meta, ok := c.ProviderMeta(name); ok && meta.API != "" && !strings.Contains(meta.API, "${") {
		return meta.API, nil
	}
	// 先按 provider 名查表、再按 route 查是有意的:name 命中(如 deepseek)时
	// 避免错误指到 api.openai.com;矛盾配置(name=deepseek 且 route=anthropic)
	// 属配置错误,可接受。
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
	return "", fmt.Errorf("no base URL known; set base_url explicitly")
}
