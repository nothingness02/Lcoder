// pkg/llm/catalog/capability.go
package catalog

import (
	"regexp"
	"strings"
)

// FallbackCapability is the static last-resort capability declaration for
// models the catalog does not know (brand-new releases ahead of a refresh).
// Zero ContextWindow/MaxOutput means "unknown" — callers fall through to
// their own defaults; the point is to get capabilities approximately right
// instead of silently empty.
type FallbackCapability struct {
	Capabilities  []string
	ContextWindow int
	MaxOutput     int
}

type fallbackRule struct {
	route string // scoped to this route; "" matches any route
	match func(model string) bool
	cap   FallbackCapability
}

var openAIReasoningRe = regexp.MustCompile(`^o\d`)

func prefixMatcher(prefixes ...string) func(string) bool {
	return func(model string) bool {
		for _, p := range prefixes {
			if strings.HasPrefix(model, p) {
				return true
			}
		}
		return false
	}
}

// fallbackRules are evaluated in order; first hit wins. Claude rules are
// scoped to the anthropic route (a claude id arriving over an OpenAI-compat
// gateway says nothing about the gateway's own models). OpenAI/Gemini rules
// are route-agnostic because those ids travel over OpenAI-compat endpoints.
var fallbackRules = []fallbackRule{
	{"anthropic", prefixMatcher("claude-opus-4", "claude-sonnet-4", "claude-haiku-4", "claude-fable"),
		FallbackCapability{Capabilities: []string{"tools", "reasoning", "vision"}}},
	{"anthropic", prefixMatcher("claude-3-", "claude-3.5-", "claude-3.7-"),
		FallbackCapability{Capabilities: []string{"tools", "vision"}}},
	{"", func(m string) bool { return openAIReasoningRe.MatchString(m) },
		FallbackCapability{Capabilities: []string{"tools", "reasoning"}}},
	{"", prefixMatcher("gpt-4o", "gpt-4-turbo", "gpt-4.1", "gpt-4.5"),
		FallbackCapability{Capabilities: []string{"tools", "vision"}}},
	{"", prefixMatcher("gemini-2.5-"),
		FallbackCapability{Capabilities: []string{"tools", "reasoning", "vision", "audio", "video"}}},
	{"", prefixMatcher("gemini-"),
		FallbackCapability{Capabilities: []string{"tools", "vision", "audio"}}},
}

// LookupFallback returns the static capability for a model the catalog does
// not know. route is the wire (or provider name — for built-ins they agree).
func LookupFallback(route, model string) (FallbackCapability, bool) {
	m := strings.ToLower(model)
	for _, r := range fallbackRules {
		if r.route != "" && r.route != route {
			continue
		}
		if r.match(m) {
			return r.cap, true
		}
	}
	return FallbackCapability{}, false
}
