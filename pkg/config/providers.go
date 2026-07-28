package config

import (
	"os"
	"regexp"
)

// ProviderConn holds the connection settings for a single provider, mirroring
// opencode's provider options block. It lets Lcoder reach custom OpenAI-compatible
// endpoints (relays, self-hosted, region-specific bases like api.moonshot.cn)
// that the engine's default per-provider routing cannot express.
type ProviderConn struct {
	BaseURL string            `yaml:"base_url" json:"base_url,omitempty"`
	APIKey  string            `yaml:"api_key"  json:"api_key,omitempty"`
	Route   string            `yaml:"route"    json:"route,omitempty"`
	Headers map[string]string `yaml:"headers"  json:"headers,omitempty"`
}

// envRefPattern matches {env:NAME} references for interpolation.
var envRefPattern = regexp.MustCompile(`\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnvRefs replaces every {env:NAME} occurrence with the value of the NAME
// environment variable (empty string if unset), matching opencode's {env:VAR} syntax.
func expandEnvRefs(s string) string {
	return envRefPattern.ReplaceAllStringFunc(s, func(m string) string {
		name := envRefPattern.FindStringSubmatch(m)[1]
		return os.Getenv(name)
	})
}

// resolveProviders returns a copy of in with {env:VAR} references expanded in
// BaseURL, APIKey, and Header values. The input map is not mutated.
func resolveProviders(in map[string]ProviderConn) map[string]ProviderConn {
	if len(in) == 0 {
		return in
	}
	out := make(map[string]ProviderConn, len(in))
	for name, c := range in {
		resolved := ProviderConn{
			BaseURL: expandEnvRefs(c.BaseURL),
			APIKey:  expandEnvRefs(c.APIKey),
			Route:   c.Route,
		}
		if len(c.Headers) > 0 {
			resolved.Headers = make(map[string]string, len(c.Headers))
			for k, v := range c.Headers {
				resolved.Headers[k] = expandEnvRefs(v)
			}
		}
		out[name] = resolved
	}
	return out
}
