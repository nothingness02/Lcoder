package memory

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultRankerOrdersJaccard(t *testing.T) {
	r := NewDefaultRanker()
	entries := []string{
		"deployment uses kubernetes",
		"unit tests should be fast",
		"kubernetes deployment pipeline",
	}
	scores := r.Rank("kubernetes deployment", entries)
	require.Len(t, scores, 2)
	require.Equal(t, "kubernetes deployment pipeline", scores[0].Text)
	require.GreaterOrEqual(t, scores[0].Score, scores[1].Score)
}

func TestDefaultRankerFiltersByMinScore(t *testing.T) {
	r := NewDefaultRanker()
	scores := r.Rank("graphql", []string{" unrelated note", "graphql schema design"})
	require.Len(t, scores, 1)
	require.Equal(t, "graphql schema design", scores[0].Text)
}

func TestDefaultRankerExactBonus(t *testing.T) {
	r := NewDefaultRanker()
	a := r.Score("auth flow", "auth flow with oauth")
	b := r.Score("auth flow", "authentication service")
	require.Greater(t, a, b)
}

func TestDefaultRankerEmptyQuery(t *testing.T) {
	r := NewDefaultRanker()
	scores := r.Rank("", []string{"anything"})
	require.Empty(t, scores)
}

func TestDefaultRankerDuplicateTokens(t *testing.T) {
	r := NewDefaultRanker()
	// Repeating the query token in the entry should not inflate the score.
	single := r.Score("go", "go programming")
	dup := r.Score("go", "go go go programming")
	require.Less(t, dup, 1.0)
	require.Equal(t, single, dup)
}

func TestDefaultRankerWithMinScore(t *testing.T) {
	r := NewDefaultRanker().WithMinScore(0.5)
	scores := r.Rank("graphql", []string{"graphql note", "unrelated"})
	require.Len(t, scores, 1)
	require.Equal(t, "graphql note", scores[0].Text)
}

func TestDefaultRankerEmptyEntry(t *testing.T) {
	r := NewDefaultRanker()
	require.Equal(t, 0.0, r.Score("query", ""))
}

func TestDefaultRankerScoreClamping(t *testing.T) {
	r := NewDefaultRanker()
	// Exact match gives Jaccard 1.0 plus exact bonus, but total is clamped.
	require.Equal(t, 1.0, r.Score("exact", "exact"))
}

func TestDefaultRankerStableOrdering(t *testing.T) {
	r := NewDefaultRanker()
	entries := []string{"golang one", "golang two"}
	scores := r.Rank("go", entries)
	require.Len(t, scores, 2)
	// Equal scores; stable sort preserves input order.
	require.Equal(t, "golang one", scores[0].Text)
	require.Equal(t, "golang two", scores[1].Text)
}

func TestDefaultRankerStopWordsIgnored(t *testing.T) {
	r := NewDefaultRanker()
	// Stop words should not change which entries are recalled or their relative order.
	a := r.Rank("the kubernetes", []string{"kubernetes deployment", "deployment kubernetes"})
	b := r.Rank("kubernetes", []string{"kubernetes deployment", "deployment kubernetes"})
	require.Len(t, a, 2)
	require.Len(t, b, 2)
	require.Equal(t, a[0].Text, b[0].Text)
	require.Equal(t, a[1].Text, b[1].Text)
}

func TestDefaultRankerPrefixBonus(t *testing.T) {
	r := NewDefaultRanker()
	// "auth" is a prefix of "authentication", so it gets a prefix bonus.
	a := r.Score("auth", "authentication service")
	b := r.Score("auth", "service")
	require.Greater(t, a, b)
}
