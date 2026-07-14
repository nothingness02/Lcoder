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

	emptyQuery := len(q.Keywords) == 0 && len(q.Symbols) == 0

	type candidate struct {
		node  Symbol
		score float64
	}
	var candidates []candidate
	for _, sym := range snapshot.Nodes {
		if sym.Kind == NodeKindFile {
			continue
		}
		var score float64
		if emptyQuery {
			score = 1.0
		} else {
			score = ScoreNode(sym, q)
		}
		if score <= 0 {
			continue
		}
		candidates = append(candidates, candidate{node: sym, score: score})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})
	if len(candidates) > max {
		candidates = candidates[:max]
	}

	results := make([]Result, len(candidates))
	for i, c := range candidates {
		results[i] = Result{
			Node:      c.node,
			Relevance: c.score,
			Stub:      formatStub(snapshot, c.node),
		}
	}
	NormalizeScores(results)
	return results, nil
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
