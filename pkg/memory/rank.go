package memory

import (
	"sort"
	"strings"
	"unicode"
)

// RankedEntry pairs a memory entry with its relevance score.
type RankedEntry struct {
	Text  string
	Score float64
}

// Ranker scores memory entries against a query.
type Ranker interface {
	Score(query, entry string) float64
	Rank(query string, entries []string) []RankedEntry
}

// DefaultRanker is a dependency-free scorer using Jaccard + keyword bonuses.
type DefaultRanker struct {
	MinScore float64
}

// NewDefaultRanker creates a ranker with a default minimum score of 0.1.
func NewDefaultRanker() *DefaultRanker {
	return &DefaultRanker{MinScore: 0.1}
}

// WithMinScore sets the score threshold. Entries below it are discarded by Rank.
func (r *DefaultRanker) WithMinScore(min float64) *DefaultRanker {
	r.MinScore = min
	return r
}

func (r *DefaultRanker) Rank(query string, entries []string) []RankedEntry {
	if strings.TrimSpace(query) == "" {
		return nil
	}
	lowerQuery := strings.ToLower(query)
	queryTokens := tokenize(query)
	var out []RankedEntry
	for _, e := range entries {
		score := r.scoreEntry(lowerQuery, queryTokens, e)
		if score < r.MinScore {
			continue
		}
		out = append(out, RankedEntry{Text: e, Score: score})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	return out
}

func (r *DefaultRanker) Score(query, entry string) float64 {
	lowerQuery := strings.ToLower(query)
	queryTokens := tokenize(query)
	return r.scoreEntry(lowerQuery, queryTokens, entry)
}

func (r *DefaultRanker) scoreEntry(lowerQuery string, queryTokens []string, entry string) float64 {
	et := tokenize(entry)
	if len(queryTokens) == 0 || len(et) == 0 {
		return 0
	}

	qset := uniqueTokens(queryTokens)
	eset := uniqueTokens(et)
	intersection := 0
	for t := range eset {
		if _, ok := qset[t]; ok {
			intersection++
		}
	}
	union := len(qset) + len(eset) - intersection
	jaccard := float64(intersection) / float64(union)

	lowerE := strings.ToLower(entry)
	exactBonus := 0.0
	if strings.Contains(lowerE, lowerQuery) {
		exactBonus = 0.3
	}

	prefixBonus := 0.0
	for qw := range qset {
		for ew := range eset {
			if strings.HasPrefix(ew, qw) && ew != qw {
				prefixBonus += 0.05
			}
		}
	}
	if prefixBonus > 0.2 {
		prefixBonus = 0.2
	}

	score := jaccard + exactBonus + prefixBonus
	if score > 1.0 {
		score = 1.0
	}
	return score
}

func tokenize(s string) []string {
	s = strings.ToLower(s)
	return strings.FieldsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r)
	})
}

func uniqueTokens(tokens []string) map[string]struct{} {
	seen := make(map[string]struct{})
	for _, t := range tokens {
		seen[t] = struct{}{}
	}
	return seen
}
