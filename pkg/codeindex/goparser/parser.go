package goparser

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"github.com/lcoder/lcoder/pkg/codeindex"
)

// Indexer parses Go source files into a codeindex.Snapshot.
type Indexer struct {
	exclude  []string
	snapshot *codeindex.Snapshot
	root     string
}

// NewIndexer creates a Go indexer with the given exclude patterns.
func NewIndexer(exclude []string) *Indexer {
	return &Indexer{
		exclude:  exclude,
		snapshot: codeindex.NewSnapshot(),
	}
}

// Update performs a full re-parse of root.
func (idx *Indexer) Update(ctx context.Context, root string) error {
	idx.root = root
	snapshot := codeindex.NewSnapshot()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			if idx.isExcluded(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if idx.isExcluded(rel) {
			return nil
		}
		if err := idx.ParseFile(snapshot, rel, path); err != nil {
			snapshot.FailedFiles = append(snapshot.FailedFiles, rel)
		}
		return nil
	})
	if err != nil {
		return err
	}
	idx.snapshot = snapshot
	return nil
}

func (idx *Indexer) isExcluded(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, p := range idx.exclude {
		p = filepath.ToSlash(p)
		if p == "" {
			continue
		}
		if dir, ok := strings.CutSuffix(p, "/"); ok {
			if rel == dir || strings.HasPrefix(rel, dir+"/") {
				return true
			}
			continue
		}
		if matched, _ := filepath.Match(p, rel); matched {
			return true
		}
		if matched, _ := filepath.Match(p, filepath.Base(rel)); matched {
			return true
		}
	}
	return false
}

// ParseFile parses a single Go source file and appends its nodes and edges to snapshot.
func (idx *Indexer) ParseFile(snapshot *codeindex.Snapshot, rel, path string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return err
	}
	pkgPath := filepath.Dir(rel)
	if pkgPath == "." {
		pkgPath = ""
	}

	info, _ := os.Stat(path)
	if info != nil {
		snapshot.Files[rel] = codeindex.FileMeta{
			ModTime: info.ModTime(),
			Size:    info.Size(),
		}
	}

	pkgSymbol := codeindex.Symbol{
		ID:            pkgPath,
		Name:          f.Name.Name,
		Kind:          codeindex.SymbolKindPackage,
		QualifiedName: pkgPath,
		FilePath:      rel,
		Language:      "go",
		StartLine:     fset.Position(f.Package).Line,
		Docstring:     firstSentence(docText(f.Doc)),
	}
	snapshot.Nodes = append(snapshot.Nodes, pkgSymbol)
	snapshot.Edges = append(snapshot.Edges, codeindex.Edge{
		Source: pkgPath,
		Target: rel,
		Kind:   codeindex.EdgeKindContains,
	})

	fileNode := codeindex.Node{
		ID:            rel,
		Name:          filepath.Base(rel),
		Kind:          codeindex.NodeKindFile,
		QualifiedName: pkgPath,
		FilePath:      rel,
		Language:      "go",
		StartLine:     1,
	}
	snapshot.Nodes = append(snapshot.Nodes, fileNode)

	funcIDs := make(map[string]string)
	typeIDs := make(map[string]string)
	funcDecls := make(map[string]*ast.FuncDecl)

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			sym := funcSymbol(fset, d, pkgPath, rel)
			snapshot.Nodes = append(snapshot.Nodes, sym)
			snapshot.Edges = append(snapshot.Edges, codeindex.Edge{
				Source: rel,
				Target: sym.ID,
				Kind:   codeindex.EdgeKindContains,
			})
			funcIDs[sym.Name] = sym.ID
			funcDecls[sym.ID] = d
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					sym := typeSymbol(fset, d, s, pkgPath, rel)
					snapshot.Nodes = append(snapshot.Nodes, sym)
					snapshot.Edges = append(snapshot.Edges, codeindex.Edge{
						Source: rel,
						Target: sym.ID,
						Kind:   codeindex.EdgeKindContains,
					})
					typeIDs[sym.Name] = sym.ID
				case *ast.ValueSpec:
					kind := codeindex.SymbolKindVar
					if d.Tok == token.CONST {
						kind = codeindex.SymbolKindConst
					}
					for _, name := range s.Names {
						sym := codeindex.Symbol{
							ID:            symbolID(pkgPath, name.Name),
							Name:          name.Name,
							Kind:          kind,
							QualifiedName: pkgPath,
							FilePath:      rel,
							Language:      "go",
							StartLine:     fset.Position(name.Pos()).Line,
							Signature:     fmt.Sprintf("%s %s", name.Name, typeString(fset, s.Type)),
							Docstring:     firstSentence(valueSpecDoc(d, s)),
						}
						snapshot.Nodes = append(snapshot.Nodes, sym)
						snapshot.Edges = append(snapshot.Edges, codeindex.Edge{
							Source: rel,
							Target: sym.ID,
							Kind:   codeindex.EdgeKindContains,
						})
						if kind == codeindex.SymbolKindConst {
							funcIDs[sym.Name] = sym.ID
						}
					}
				}
			}
		}
	}

	// Record imports as edges from the package node.
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		snapshot.Edges = append(snapshot.Edges, codeindex.Edge{
			Source: pkgPath,
			Target: path,
			Kind:   codeindex.EdgeKindImports,
		})
	}

	// Extract same-package call edges from function bodies.
	for id, d := range funcDecls {
		if d.Body == nil {
			continue
		}
		ast.Inspect(d.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				if callee, ok := funcIDs[fun.Name]; ok {
					snapshot.Edges = append(snapshot.Edges, codeindex.Edge{
						Source: id,
						Target: callee,
						Kind:   codeindex.EdgeKindCalls,
						Line:   fset.Position(fun.Pos()).Line,
					})
				}
			case *ast.SelectorExpr:
				if xIdent, ok := fun.X.(*ast.Ident); ok {
					key := xIdent.Name + "." + fun.Sel.Name
					if callee, ok := funcIDs[key]; ok {
						snapshot.Edges = append(snapshot.Edges, codeindex.Edge{
							Source: id,
							Target: callee,
							Kind:   codeindex.EdgeKindCalls,
							Line:   fset.Position(fun.Pos()).Line,
						})
					} else if _, isType := typeIDs[xIdent.Name]; isType {
						// Method-style call on a package-level type, e.g. Type.Method().
						// Already covered if the method was indexed.
					} else {
						// Cross-package or unresolved reference.
						snapshot.Edges = append(snapshot.Edges, codeindex.Edge{
							Source: id,
							Target: key,
							Kind:   codeindex.EdgeKindReferences,
							Line:   fset.Position(fun.Pos()).Line,
						})
					}
				}
			}
			return true
		})
	}

	return nil
}

func symbolID(pkgPath, name string) string {
	if pkgPath == "" {
		return name
	}
	return pkgPath + "." + name
}

func methodID(pkgPath, recvType, name string) string {
	if pkgPath == "" {
		return recvType + "." + name
	}
	return pkgPath + "." + recvType + "." + name
}

func funcSymbol(fset *token.FileSet, d *ast.FuncDecl, pkgPath, rel string) codeindex.Symbol {
	name := d.Name.Name
	id := symbolID(pkgPath, name)
	kind := codeindex.SymbolKindFunc
	if d.Recv != nil && len(d.Recv.List) > 0 {
		kind = codeindex.SymbolKindMethod
		recvType := receiverType(d.Recv.List[0].Type)
		id = methodID(pkgPath, recvType, name)
		name = recvType + "." + name
	}
	sig := typeString(fset, d.Type)
	if strings.HasPrefix(sig, "func") {
		sig = "func " + name + sig[4:]
	} else {
		sig = "func " + name + sig
	}
	return codeindex.Symbol{
		ID:            id,
		Name:          name,
		Kind:          kind,
		QualifiedName: pkgPath,
		FilePath:      rel,
		Language:      "go",
		StartLine:     fset.Position(d.Pos()).Line,
		Signature:     sig,
		Docstring:     firstSentence(docText(d.Doc)),
	}
}

func typeSymbol(fset *token.FileSet, gd *ast.GenDecl, s *ast.TypeSpec, pkgPath, rel string) codeindex.Symbol {
	doc := s.Doc
	if doc == nil {
		doc = gd.Doc
	}
	return codeindex.Symbol{
		ID:            symbolID(pkgPath, s.Name.Name),
		Name:          s.Name.Name,
		Kind:          codeindex.SymbolKindType,
		QualifiedName: pkgPath,
		FilePath:      rel,
		Language:      "go",
		StartLine:     fset.Position(s.Pos()).Line,
		Signature:     fmt.Sprintf("type %s %s", s.Name.Name, typeString(fset, s.Type)),
		Docstring:     firstSentence(docText(doc)),
	}
}

func receiverType(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverType(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return receiverType(t.X)
	}
	return ""
}

func typeString(fset *token.FileSet, expr ast.Expr) string {
	if expr == nil {
		return ""
	}
	var b bytes.Buffer
	if err := format.Node(&b, fset, expr); err != nil {
		return ""
	}
	return b.String()
}

func docText(cg *ast.CommentGroup) string {
	if cg == nil {
		return ""
	}
	return cg.Text()
}

func valueSpecDoc(gd *ast.GenDecl, vs *ast.ValueSpec) string {
	if vs.Doc != nil {
		return vs.Doc.Text()
	}
	if gd.Doc != nil {
		return gd.Doc.Text()
	}
	return ""
}

func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, ".\n"); i > 0 {
		return s[:i+1]
	}
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}

func (idx *Indexer) Clear() error {
	idx.snapshot = codeindex.NewSnapshot()
	return nil
}

func (idx *Indexer) Search(ctx context.Context, q codeindex.Query) ([]codeindex.Result, error) {
	return codeindex.SearchSnapshot(idx.snapshot, q)
}
