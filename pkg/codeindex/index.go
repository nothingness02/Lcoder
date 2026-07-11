package codeindex

import "context"

// SymbolKind classifies a code symbol.
type SymbolKind string

const (
	SymbolKindPackage SymbolKind = "package"
	SymbolKindType    SymbolKind = "type"
	SymbolKindFunc    SymbolKind = "func"
	SymbolKindMethod  SymbolKind = "method"
	SymbolKindVar     SymbolKind = "var"
	SymbolKindConst   SymbolKind = "const"
)

// Symbol represents a single named entity extracted from source code.
type Symbol struct {
	ID        string
	Name      string
	Kind      SymbolKind
	Package   string
	File      string
	Line      int
	Signature string
	Doc       string
}

// Relation links two symbols (e.g. caller -> callee).
type Relation struct {
	From string
	To   string
	Kind string
}

// Query describes a search request.
type Query struct {
	Keywords     []string
	Symbols      []string
	Kinds        []SymbolKind
	MaxResults   int
	IncludeTests bool
}

// Result is one matching symbol with a formatted stub.
type Result struct {
	Symbol    Symbol
	Relevance float64
	Stub      string
}

// Indexer builds and searches a repository code index.
type Indexer interface {
	Update(ctx context.Context, root string) error
	Search(ctx context.Context, q Query) ([]Result, error)
	Clear() error
}
