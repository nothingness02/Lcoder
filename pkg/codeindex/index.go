package codeindex

import "context"

// NodeKind classifies a code graph node.
type NodeKind string

const (
	NodeKindFile       NodeKind = "file"
	NodeKindPackage    NodeKind = "package"
	NodeKindModule     NodeKind = "module"
	NodeKindClass      NodeKind = "class"
	NodeKindInterface  NodeKind = "interface"
	NodeKindStruct     NodeKind = "struct"
	NodeKindTrait      NodeKind = "trait"
	NodeKindProtocol   NodeKind = "protocol"
	NodeKindFunction   NodeKind = "function"
	NodeKindMethod     NodeKind = "method"
	NodeKindProperty   NodeKind = "property"
	NodeKindField      NodeKind = "field"
	NodeKindVariable   NodeKind = "variable"
	NodeKindConstant   NodeKind = "constant"
	NodeKindEnum       NodeKind = "enum"
	NodeKindEnumMember NodeKind = "enum_member"
	NodeKindTypeAlias  NodeKind = "type_alias"
	NodeKindNamespace  NodeKind = "namespace"
	NodeKindExport     NodeKind = "export"
	NodeKindImport     NodeKind = "import"
	NodeKindRoute      NodeKind = "route"
	NodeKindComponent  NodeKind = "component"
)

// SymbolKind is the historical name for NodeKind. Kept for compatibility.
type SymbolKind = NodeKind

// Historical kind constants aliased to the NodeKind values used by the
// language parsers.
const (
	SymbolKindPackage SymbolKind = NodeKindPackage
	SymbolKindType    SymbolKind = NodeKindClass
	SymbolKindFunc    SymbolKind = NodeKindFunction
	SymbolKindMethod  SymbolKind = NodeKindMethod
	SymbolKindVar     SymbolKind = NodeKindVariable
	SymbolKindConst   SymbolKind = NodeKindConstant
)

// Node represents a single named entity extracted from source code.
// It is aligned with CodeGraph's node model so richer extractors can be
// plugged in later without changing the storage or search layers.
type Node struct {
	ID             string   `json:"id"`
	Kind           NodeKind `json:"kind"`
	Name           string   `json:"name"`
	QualifiedName  string   `json:"qualified_name"`
	FilePath       string   `json:"file_path"`
	Language       string   `json:"language"`
	StartLine      int      `json:"start_line"`
	EndLine        int      `json:"end_line"`
	StartColumn    int      `json:"start_column"`
	EndColumn      int      `json:"end_column"`
	Docstring      string   `json:"docstring,omitempty"`
	Signature      string   `json:"signature,omitempty"`
	Visibility     string   `json:"visibility,omitempty"`
	IsExported     bool     `json:"is_exported,omitempty"`
	IsAsync        bool     `json:"is_async,omitempty"`
	IsStatic       bool     `json:"is_static,omitempty"`
	IsAbstract     bool     `json:"is_abstract,omitempty"`
	Decorators     []string `json:"decorators,omitempty"`
	TypeParameters []string `json:"type_parameters,omitempty"`
	ReturnType     string   `json:"return_type,omitempty"`
	UpdatedAt      int64    `json:"updated_at"`
}

// Symbol is the historical name for Node. Kept for compatibility with callers
// that expect the old field name.
type Symbol = Node

// EdgeKind classifies a relationship between two nodes.
type EdgeKind string

const (
	EdgeKindContains    EdgeKind = "contains"
	EdgeKindCalls       EdgeKind = "calls"
	EdgeKindImports     EdgeKind = "imports"
	EdgeKindExports     EdgeKind = "exports"
	EdgeKindExtends     EdgeKind = "extends"
	EdgeKindImplements  EdgeKind = "implements"
	EdgeKindReferences  EdgeKind = "references"
	EdgeKindInstantiates EdgeKind = "instantiates"
	EdgeKindOverrides   EdgeKind = "overrides"
	EdgeKindDecorates   EdgeKind = "decorates"
	EdgeKindTypeOf      EdgeKind = "type_of"
)

// Edge represents a directed relationship between two nodes.
type Edge struct {
	Source    string         `json:"source"`
	Target    string         `json:"target"`
	Kind      EdgeKind       `json:"kind"`
	Line      int            `json:"line,omitempty"`
	Column    int            `json:"column,omitempty"`
	Provenance string        `json:"provenance,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// Relation is the historical name for Edge. Kept for compatibility.
type Relation = Edge

// Query describes a search request.
type Query struct {
	Keywords     []string
	Symbols      []string
	Kinds        []NodeKind
	Phrase       string // original lowercased query text, used for AND-first FTS
	MaxResults   int
	IncludeTests bool
}

// Result is one matching node with a formatted stub.
type Result struct {
	Node      Node
	Relevance float64
	Stub      string
}

// Indexer builds and searches a repository code index.
type Indexer interface {
	Update(ctx context.Context, root string) error
	Search(ctx context.Context, q Query) ([]Result, error)
	Clear() error
}
