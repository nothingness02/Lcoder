package codeindex

import (
	"context"
	"fmt"
	"sort"
)

// GraphStore is the subset of the SQLite store that the ContextBuilder needs.
// It is implemented by sqlitestore.Indexer.
type GraphStore interface {
	Search(ctx context.Context, q Query) ([]Result, error)
	NodeByID(ctx context.Context, id string) (Node, bool, error)
	Neighbors(ctx context.Context, nodeID string, kinds []EdgeKind, direction string) ([]Edge, error)
}

// ContextBuilder turns a natural-language query into a set of ranked code stubs
// by combining hybrid search with bounded graph traversal. It mirrors the
// CodeGraph context builder: find seed symbols, expand along call/reference
// edges, and return the most relevant surrounding code.
type ContextBuilder struct {
	store GraphStore
}

// NewContextBuilder creates a builder bound to a graph store.
func NewContextBuilder(store GraphStore) *ContextBuilder {
	return &ContextBuilder{store: store}
}

// BuildOptions controls how the context graph is expanded.
type BuildOptions struct {
	MaxSeeds   int
	MaxDepth   int
	MaxNodes   int
	EdgeKinds  []EdgeKind
	Directions []string // "in", "out", "both"
}

// DefaultBuildOptions returns sensible defaults for repo exploration.
func DefaultBuildOptions() BuildOptions {
	return BuildOptions{
		MaxSeeds:  5,
		MaxDepth:  2,
		MaxNodes:  30,
		EdgeKinds: []EdgeKind{EdgeKindCalls, EdgeKindReferences, EdgeKindContains, EdgeKindExtends, EdgeKindImplements, EdgeKindInstantiates, EdgeKindOverrides},
		Directions: []string{"both"},
	}
}

// Build returns ranked stubs for the query plus related symbols.
func (b *ContextBuilder) Build(ctx context.Context, query string, opts BuildOptions) ([]Result, error) {
	if opts.MaxSeeds <= 0 {
		opts.MaxSeeds = 5
	}
	if opts.MaxDepth < 0 {
		opts.MaxDepth = 0
	}
	if opts.MaxNodes <= 0 {
		opts.MaxNodes = 30
	}
	if len(opts.EdgeKinds) == 0 {
		opts.EdgeKinds = DefaultBuildOptions().EdgeKinds
	}
	if len(opts.Directions) == 0 {
		opts.Directions = []string{"both"}
	}

	keywords := splitQuery(query)
	seeds, err := b.store.Search(ctx, Query{
		Keywords:   keywords,
		MaxResults: opts.MaxSeeds,
	})
	if err != nil {
		return nil, fmt.Errorf("seed search: %w", err)
	}

	collected := make(map[string]Result)
	for _, r := range seeds {
		collected[r.Node.ID] = r
	}

	// Bounded BFS expansion from seeds.
	frontier := make(map[string]bool)
	for _, r := range seeds {
		frontier[r.Node.ID] = true
	}
	for depth := 0; depth < opts.MaxDepth && len(collected) < opts.MaxNodes; depth++ {
		nextFrontier := make(map[string]bool)
		for id := range frontier {
			for _, dir := range opts.Directions {
				edges, err := b.store.Neighbors(ctx, id, opts.EdgeKinds, dir)
				if err != nil {
					return nil, fmt.Errorf("graph neighbors: %w", err)
				}
				for _, e := range edges {
					nextID := e.Target
					if dir == "in" {
						nextID = e.Source
					} else if dir == "both" {
						if e.Source == id {
							nextID = e.Target
						} else {
							nextID = e.Source
						}
					}
					if _, ok := collected[nextID]; ok {
						continue
					}
					node, ok, err := b.store.NodeByID(ctx, nextID)
					if err != nil {
						return nil, fmt.Errorf("lookup neighbor: %w", err)
					}
					if !ok {
						continue
					}
					collected[node.ID] = Result{
						Node:      node,
						Relevance: 0.5,
						Stub:      formatNodeStub(node),
					}
					nextFrontier[node.ID] = true
					if len(collected) >= opts.MaxNodes {
						break
					}
				}
			}
		}
		frontier = nextFrontier
	}

	results := make([]Result, 0, len(collected))
	for _, r := range collected {
		results = append(results, r)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Relevance > results[j].Relevance
	})
	return results, nil
}

func formatNodeStub(n Node) string {
	if n.Signature == "" {
		return fmt.Sprintf("// %s:%d\n%s %s", n.FilePath, n.StartLine, n.Kind, n.Name)
	}
	return fmt.Sprintf("// %s:%d\n%s", n.FilePath, n.StartLine, n.Signature)
}
