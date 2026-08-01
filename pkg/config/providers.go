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
	// APIKeys is a failover pool: the engine rotates these keys and benches
	// failing ones. When empty, APIKey is the single credential.
	APIKeys []string `yaml:"api_keys" json:"api_keys,omitempty"`
	Route   string   `yaml:"route"    json:"route,omitempty"`
	// Protocol explicitly declares the wire protocol (openai-chat |
	// openai-responses | anthropic); empty derives it from route. Unknown
	// values are rejected at startup, never silently defaulted.
	Protocol string            `yaml:"protocol" json:"protocol,omitempty"`
	Headers  map[string]string `yaml:"headers"  json:"headers,omitempty"`
	// MaxConcurrent caps concurrent streams to this provider (0 = unlimited).
	MaxConcurrent int `yaml:"max_concurrent" json:"max_concurrent,omitempty"`
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
// BaseURL, APIKey, and Header values. The input map is not mutated. The second
// return lists every reference whose variable is unset, as "provider:VAR" —
// a typo'd variable name must surface as a startup warning, not a silent
// empty credential.
func resolveProviders(in map[string]ProviderConn) (map[string]ProviderConn, []string) {
	if len(in) == 0 {
		return in, nil
	}
	var missing []string
	report := func(provider, s string) {
		for _, m := range envRefPattern.FindAllStringSubmatch(s, -1) {
			if os.Getenv(m[1]) == "" {
				missing = append(missing, provider+":"+m[1])
			}
		}
	}
	out := make(map[string]ProviderConn, len(in))
	for name, c := range in {
		report(name, c.BaseURL)
		report(name, c.APIKey)
		// Copy the whole struct so api_keys / protocol / max_concurrent
		// survive, then expand env references field by field.
		resolved := c
		resolved.BaseURL = expandEnvRefs(c.BaseURL)
		resolved.APIKey = expandEnvRefs(c.APIKey)
		if len(c.APIKeys) > 0 {
			resolved.APIKeys = make([]string, len(c.APIKeys))
			for i, k := range c.APIKeys {
				report(name, k)
				resolved.APIKeys[i] = expandEnvRefs(k)
			}
		}
		if len(c.Headers) > 0 {
			resolved.Headers = make(map[string]string, len(c.Headers))
			for k, v := range c.Headers {
				report(name, v)
				resolved.Headers[k] = expandEnvRefs(v)
			}
		}
		out[name] = resolved
	}
	return out, missing
}

// missingMCPEnvRefs lists the unset variable names referenced by an MCP
// server's url/headers/env settings.
func missingMCPEnvRefs(s MCPServerConfig) []string {
	var missing []string
	report := func(v string) {
		for _, m := range envRefPattern.FindAllStringSubmatch(v, -1) {
			if os.Getenv(m[1]) == "" {
				missing = append(missing, m[1])
			}
		}
	}
	report(s.URL)
	for _, v := range s.Headers {
		report(v)
	}
	for _, v := range s.Env {
		report(v)
	}
	return missing
}
