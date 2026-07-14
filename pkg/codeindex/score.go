package codeindex

import (
	"strings"
	"unicode"
)

// Scoring weights shared between the in-memory snapshot search and the SQLite
// store search. Tuned so that exact symbol matches dominate, name matches beat
// signature/docstring matches, and structural symbols beat incidental fields.
var (
	exactSymbolWeight = 50.0
	nameEqualWeight   = 15.0
	nameContainsWeight = 10.0
	namePrefixWeight   = 5.0
	nameSuffixWeight   = 3.0
	qualContainsWeight = 4.0
	qualSuffixWeight   = 3.0
	sigContainsWeight  = 2.0
	docContainsWeight  = 1.0
)

// KindWeight biases the final score toward structural/entry-point symbols.
func KindWeight(k NodeKind) float64 {
	switch k {
	case NodeKindFunction, NodeKindMethod:
		return 1.0
	case NodeKindClass, NodeKindStruct, NodeKindInterface, NodeKindTrait, NodeKindProtocol:
		return 1.0
	case NodeKindPackage, NodeKindModule, NodeKindNamespace:
		return 0.6
	case NodeKindField, NodeKindProperty, NodeKindVariable, NodeKindConstant, NodeKindEnumMember:
		return 0.7
	case NodeKindTypeAlias, NodeKindEnum:
		return 0.8
	case NodeKindImport, NodeKindExport:
		return 0.4
	case NodeKindRoute, NodeKindComponent:
		return 0.9
	default:
		return 0.8
	}
}

// ScoreNode returns a relevance score for a node against a query. The score is
// not normalized; callers use NormalizeScores after ranking/truncation.
func ScoreNode(n Node, q Query) float64 {
	if len(q.Kinds) > 0 && !kindAllowed(n.Kind, q.Kinds) {
		return 0
	}

	score := 0.0
	for _, s := range q.Symbols {
		if strings.EqualFold(n.ID, s) || strings.EqualFold(n.Name, s) || strings.EqualFold(n.QualifiedName, s) {
			score += exactSymbolWeight
		}
	}

	name := strings.ToLower(n.Name)
	qual := strings.ToLower(n.QualifiedName)
	sig := strings.ToLower(n.Signature)
	doc := strings.ToLower(n.Docstring)

	for _, kw := range q.Keywords {
		kw = strings.ToLower(kw)
		if kw == "" {
			continue
		}
		if name == kw {
			score += nameEqualWeight
		} else if strings.Contains(name, kw) {
			score += nameContainsWeight
		}
		if strings.HasPrefix(name, kw) {
			score += namePrefixWeight
		}
		if strings.HasSuffix(name, kw) {
			score += nameSuffixWeight
		}
		if strings.Contains(qual, kw) {
			score += qualContainsWeight
		}
		if strings.HasSuffix(qual, "."+kw) {
			score += qualSuffixWeight
		}
		if strings.Contains(sig, kw) {
			score += sigContainsWeight
		}
		if strings.Contains(doc, kw) {
			score += docContainsWeight
		}
	}

	return score * KindWeight(n.Kind)
}

// NormalizeScores divides all relevance scores by the maximum, so the top
// result becomes 1.0. It is a no-op when the max is zero.
func NormalizeScores(results []Result) {
	if len(results) == 0 {
		return
	}
	max := results[0].Relevance
	for _, r := range results[1:] {
		if r.Relevance > max {
			max = r.Relevance
		}
	}
	if max <= 0 {
		return
	}
	for i := range results {
		results[i].Relevance /= max
	}
}

// ExpandIdentifier takes an identifier/token and returns a set of search
// keywords that cover the original text plus camelCase and snake_case splits.
// For example, "RunAgent" expands to {"runagent", "run", "agent"} and
// "foo_bar" expands to {"foo_bar", "foobar", "foo", "bar"}.
func ExpandIdentifier(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	seen := map[string]bool{}
	lower := strings.ToLower(s)
	seen[lower] = true

	// Split on separators and accumulate a concatenated form.
	sepParts := strings.FieldsFunc(lower, func(r rune) bool {
		return r == '_' || r == '-' || r == '.' || r == '/'
	})
	var concat []byte
	for _, p := range sepParts {
		if p == "" {
			continue
		}
		seen[p] = true
		concat = append(concat, []byte(p)...)
	}
	if len(concat) > 0 {
		seen[string(concat)] = true
	}

	// Split camelCase on the original mixed-case string.
	var camelParts []string
	var cur []rune
	runes := []rune(s)
	for i, r := range runes {
		if i == 0 {
			cur = append(cur, r)
			continue
		}
		prev := runes[i-1]
		// Split on lowercase-to-uppercase transitions.
		if unicode.IsUpper(r) && unicode.IsLower(prev) {
			camelParts = append(camelParts, string(cur))
			cur = nil
		}
		// Split when an acronym ends: uppercase run followed by lowercase starts.
		if i+1 < len(runes) && unicode.IsUpper(r) && unicode.IsUpper(prev) && unicode.IsLower(runes[i+1]) {
			camelParts = append(camelParts, string(cur))
			cur = []rune{r}
			continue
		}
		cur = append(cur, r)
	}
	if len(cur) > 0 {
		camelParts = append(camelParts, string(cur))
	}
	var camelConcat []byte
	for _, p := range camelParts {
		lp := strings.ToLower(p)
		if lp != "" {
			seen[lp] = true
			camelConcat = append(camelConcat, []byte(lp)...)
		}
	}
	if len(camelConcat) > 0 {
		seen[string(camelConcat)] = true
	}

	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}

// SplitQuery tokenizes a query string and expands each token. It is used by
// callers that only need a flat keyword list.
func SplitQuery(s string) []string {
	_, kws := ParseQuery(s)
	return kws
}

// ParseQuery returns the original lowercased phrase and a unique set of
// expanded keywords suitable for scoring and FTS.
func ParseQuery(s string) (phrase string, keywords []string) {
	s = strings.TrimSpace(s)
	phrase = strings.ToLower(s)
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || r == '.' || r == '/' || r == '_' || r == '-'
	}) {
		for _, kw := range ExpandIdentifier(f) {
			if !seen[kw] {
				seen[kw] = true
				keywords = append(keywords, kw)
			}
		}
	}
	return phrase, keywords
}

func kindAllowed(k NodeKind, allowed []NodeKind) bool {
	for _, a := range allowed {
		if a == k {
			return true
		}
	}
	return false
}
