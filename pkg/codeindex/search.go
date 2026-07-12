package codeindex

import (
	"fmt"
	"sort"
	"strings"
)

// SearchSnapshot searches a snapshot for symbols matching q and returns ranked
// results with formatted stubs. It is shared by all language-specific indexers
// and the multi-language dispatcher.
func SearchSnapshot(snapshot *Snapshot, q Query) ([]Result, error) {
	if snapshot == nil {
		return nil, nil
	}
	max := q.MaxResults
	if max <= 0 {
		max = 10
	}
	keywords := normalizeKeywords(q.Keywords)
	exact := normalizeSymbols(q.Symbols)
	emptyQuery := len(keywords) == 0 && len(exact) == 0

	var results []Result
	for _, sym := range snapshot.Nodes {
		if sym.Kind == NodeKindFile {
			continue
		}
		score := scoreSymbol(sym, keywords, exact)
		if emptyQuery {
			score = 1.0
		}
		if score <= 0 {
			continue
		}
		results = append(results, Result{
			Node:      sym,
			Relevance: score,
			Stub:      formatStub(snapshot, sym),
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Relevance > results[j].Relevance
	})
	if len(results) > max {
		results = results[:max]
	}
	return results, nil
}

func normalizeKeywords(words []string) []string {
	var out []string
	for _, w := range words {
		w = strings.ToLower(strings.TrimSpace(w))
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

func normalizeSymbols(syms []string) []string {
	var out []string
	for _, s := range syms {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func scoreSymbol(sym Symbol, keywords []string, exact []string) float64 {
	score := 0.0
	text := strings.ToLower(sym.Name + " " + sym.QualifiedName + " " + sym.Docstring + " " + sym.Signature)
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			score += 1.0
		}
		if strings.EqualFold(sym.Name, kw) {
			score += 3.0
		}
	}
	for _, e := range exact {
		if strings.EqualFold(sym.ID, e) || strings.EqualFold(sym.Name, e) {
			score += 5.0
		}
	}
	return score
}

func formatStub(snapshot *Snapshot, sym Symbol) string {
	var b strings.Builder
	fmt.Fprintf(&b, "// %s:%d\n%s", sym.FilePath, sym.StartLine, sym.Signature)
	related := relatedSymbols(snapshot, sym.ID, 3)
	if len(related) > 0 {
		b.WriteString("\n// Related: " + strings.Join(related, ", "))
	}
	return b.String()
}

func relatedSymbols(snapshot *Snapshot, id string, max int) []string {
	if snapshot == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range snapshot.Edges {
		if r.Source == id && !seen[r.Target] {
			seen[r.Target] = true
			out = append(out, r.Target)
		}
		if r.Target == id && !seen[r.Source] {
			seen[r.Source] = true
			out = append(out, r.Source)
		}
		if len(out) >= max {
			break
		}
	}
	return out
}
