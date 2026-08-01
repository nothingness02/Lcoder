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
	Protocol provider.Protocol
	BaseURL  string
	Guessed  bool // Protocol was inferred, not declared
}

// ResolveProvider decides the wire and endpoint for one provider entry
// (mirrors kimi-code resolveCatalogImport, cut down to three wires). An
// explicit connProtocol is validated strictly (unknown values are config
// errors, never silently defaulted); when empty, the protocol is inferred
// from the provider name and models.dev metadata (anthropic/claude →
// anthropic, codex → openai-responses, else openai-chat). A missing base URL
// falls back to the catalog api, then built-in defaults; a blank or
// placeholder base URL is rejected — silently sending the key to the wrong
// host is a credential leak.
func (c *Catalog) ResolveProvider(name, connProtocol, connBaseURL string) (ResolvedProvider, error) {
	proto := provider.Protocol("")
	guessed := false
	if connProtocol != "" {
		p, err := provider.ParseProtocol(connProtocol)
		if err != nil {
			return ResolvedProvider{}, err
		}
		proto = p
	} else {
		proto = c.inferProtocol(name)
		guessed = true
	}
	base, err := c.resolveBase(name, connBaseURL)
	if err != nil {
		return ResolvedProvider{}, err
	}
	return ResolvedProvider{Protocol: proto, BaseURL: base, Guessed: guessed}, nil
}

func (c *Catalog) inferProtocol(name string) provider.Protocol {
	lower := strings.ToLower(name)
	npm := ""
	if meta, ok := c.ProviderMeta(name); ok {
		npm = strings.ToLower(meta.Npm)
	}
	if strings.Contains(npm, "anthropic") || strings.Contains(lower, "anthropic") || strings.Contains(lower, "claude") {
		return provider.ProtocolAnthropic
	}
	if strings.Contains(npm, "codex") || strings.Contains(lower, "codex") {
		return provider.ProtocolOpenAIResponses
	}
	return provider.InferProtocol(name)
}

func (c *Catalog) resolveBase(name, explicit string) (string, error) {
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
	if d := provider.DefaultBaseURL(name); d != "" {
		return d, nil
	}
	return "", fmt.Errorf("no base URL known; set base_url explicitly")
}
