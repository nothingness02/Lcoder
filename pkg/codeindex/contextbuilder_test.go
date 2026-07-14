package codeindex

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeGraphStore struct {
	searchResults []Result
	edges         []Edge
	nodeByID      map[string]Node
	neighborCalls [][]string
}

func (f *fakeGraphStore) Search(ctx context.Context, q Query) ([]Result, error) {
	return f.searchResults, nil
}

func (f *fakeGraphStore) NodeByID(ctx context.Context, id string) (Node, bool, error) {
	n, ok := f.nodeByID[id]
	return n, ok, nil
}

func (f *fakeGraphStore) Neighbors(ctx context.Context, nodeIDs []string, kinds []EdgeKind, direction string) ([]Edge, error) {
	f.neighborCalls = append(f.neighborCalls, append([]string(nil), nodeIDs...))
	var out []Edge
	allowed := make(map[EdgeKind]bool)
	for _, k := range kinds {
		allowed[k] = true
	}
	for _, nodeID := range nodeIDs {
		for _, e := range f.edges {
			if !allowed[e.Kind] {
				continue
			}
			if direction == "out" && e.Source != nodeID {
				continue
			}
			if direction == "in" && e.Target != nodeID {
				continue
			}
			if direction == "both" && e.Source != nodeID && e.Target != nodeID {
				continue
			}
			out = append(out, e)
		}
	}
	return out, nil
}

func TestContextBuilderExpandsAlongEdges(t *testing.T) {
	store := &fakeGraphStore{
		searchResults: []Result{
			{Node: Node{ID: "A", Name: "A", FilePath: "a.go", StartLine: 1, Signature: "func A()"}, Relevance: 10},
		},
		edges: []Edge{
			{Source: "A", Target: "B", Kind: EdgeKindCalls},
		},
		nodeByID: map[string]Node{
			"A": {ID: "A", Name: "A", FilePath: "a.go", StartLine: 1, Signature: "func A()"},
			"B": {ID: "B", Name: "B", FilePath: "b.go", StartLine: 2, Signature: "func B()"},
		},
	}

	builder := NewContextBuilder(store)
	res, err := builder.Build(context.Background(), "A", BuildOptions{
		MaxSeeds:   1,
		MaxDepth:   1,
		MaxNodes:   10,
		EdgeKinds:  []EdgeKind{EdgeKindCalls},
		Directions: []string{"out"},
	})
	require.NoError(t, err)
	require.Len(t, res, 2)

	names := make(map[string]bool)
	for _, r := range res {
		names[r.Node.Name] = true
	}
	require.Contains(t, names, "A")
	require.Contains(t, names, "B")
}

func TestContextBuilderBatchNeighbors(t *testing.T) {
	store := &fakeGraphStore{
		searchResults: []Result{
			{Node: Node{ID: "A", Name: "A", FilePath: "a.go", StartLine: 1, Signature: "func A()"}, Relevance: 1.0},
			{Node: Node{ID: "B", Name: "B", FilePath: "b.go", StartLine: 1, Signature: "func B()"}, Relevance: 1.0},
		},
		edges: []Edge{
			{Source: "A", Target: "C", Kind: EdgeKindCalls},
			{Source: "B", Target: "D", Kind: EdgeKindCalls},
		},
		nodeByID: map[string]Node{
			"A": {ID: "A", Name: "A", FilePath: "a.go", StartLine: 1, Signature: "func A()"},
			"B": {ID: "B", Name: "B", FilePath: "b.go", StartLine: 1, Signature: "func B()"},
			"C": {ID: "C", Name: "C", FilePath: "c.go", StartLine: 1, Signature: "func C()"},
			"D": {ID: "D", Name: "D", FilePath: "d.go", StartLine: 1, Signature: "func D()"},
		},
	}

	builder := NewContextBuilder(store)
	_, err := builder.Build(context.Background(), "A B", BuildOptions{
		MaxSeeds:   2,
		MaxDepth:   1,
		MaxNodes:   10,
		EdgeKinds:  []EdgeKind{EdgeKindCalls},
		Directions: []string{"out"},
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(store.neighborCalls), 1)
	require.ElementsMatch(t, []string{"A", "B"}, store.neighborCalls[0])
}

func TestContextBuilderDepthDecay(t *testing.T) {
	store := &fakeGraphStore{
		searchResults: []Result{
			{Node: Node{ID: "A", Name: "A", FilePath: "a.go", StartLine: 1, Signature: "func A()"}, Relevance: 1.0},
		},
		edges: []Edge{
			{Source: "A", Target: "B", Kind: EdgeKindCalls},
			{Source: "B", Target: "C", Kind: EdgeKindCalls},
		},
		nodeByID: map[string]Node{
			"A": {ID: "A", Name: "A", FilePath: "a.go", StartLine: 1, Signature: "func A()"},
			"B": {ID: "B", Name: "B", FilePath: "b.go", StartLine: 1, Signature: "func B()"},
			"C": {ID: "C", Name: "C", FilePath: "c.go", StartLine: 1, Signature: "func C()"},
		},
	}

	builder := NewContextBuilder(store)
	res, err := builder.Build(context.Background(), "A", BuildOptions{
		MaxSeeds:   1,
		MaxDepth:   2,
		MaxNodes:   10,
		EdgeKinds:  []EdgeKind{EdgeKindCalls},
		Directions: []string{"out"},
	})
	require.NoError(t, err)
	require.Len(t, res, 3)

	scores := make(map[string]float64)
	for _, r := range res {
		scores[r.Node.Name] = r.Relevance
	}
	require.Greater(t, scores["A"], scores["B"])
	require.Greater(t, scores["B"], scores["C"])
}

func TestContextBuilderEdgeWeights(t *testing.T) {
	store := &fakeGraphStore{
		searchResults: []Result{
			{Node: Node{ID: "A", Name: "A", FilePath: "a.go", StartLine: 1, Signature: "func A()"}, Relevance: 1.0},
		},
		edges: []Edge{
			{Source: "A", Target: "B", Kind: EdgeKindCalls},
			{Source: "A", Target: "C", Kind: EdgeKindContains},
		},
		nodeByID: map[string]Node{
			"A": {ID: "A", Name: "A", FilePath: "a.go", StartLine: 1, Signature: "func A()"},
			"B": {ID: "B", Name: "B", FilePath: "b.go", StartLine: 1, Signature: "func B()"},
			"C": {ID: "C", Name: "C", FilePath: "c.go", StartLine: 1, Signature: "func C()"},
		},
	}

	builder := NewContextBuilder(store)
	res, err := builder.Build(context.Background(), "A", BuildOptions{
		MaxSeeds:   1,
		MaxDepth:   1,
		MaxNodes:   10,
		EdgeKinds:  []EdgeKind{EdgeKindCalls, EdgeKindContains},
		Directions: []string{"out"},
	})
	require.NoError(t, err)
	require.Len(t, res, 3)

	scores := make(map[string]float64)
	for _, r := range res {
		scores[r.Node.Name] = r.Relevance
	}
	require.Greater(t, scores["B"], scores["C"], "calls edge should outweigh contains edge")
}

func TestContextBuilderNoEdgesReturnsSeeds(t *testing.T) {
	store := &fakeGraphStore{
		searchResults: []Result{
			{Node: Node{ID: "X", Name: "X", FilePath: "x.go", StartLine: 1, Signature: "func X()"}, Relevance: 5},
		},
		nodeByID: map[string]Node{
			"X": {ID: "X", Name: "X", FilePath: "x.go", StartLine: 1, Signature: "func X()"},
		},
	}

	builder := NewContextBuilder(store)
	res, err := builder.Build(context.Background(), "X", DefaultBuildOptions())
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.Equal(t, "X", res[0].Node.Name)
}
