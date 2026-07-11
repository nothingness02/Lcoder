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

// Update performs a full re-parse of root. Incremental updates are left for later optimization.
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
		// Directory prefix: "dir/" matches the directory itself and anything inside.
		if dir, ok := strings.CutSuffix(p, "/"); ok {
			if rel == dir || strings.HasPrefix(rel, dir+"/") {
				return true
			}
			continue
		}
		// Glob match against the full relative path or the base name.
		if matched, _ := filepath.Match(p, rel); matched {
			return true
		}
		if matched, _ := filepath.Match(p, filepath.Base(rel)); matched {
			return true
		}
	}
	return false
}

// ParseFile parses a single Go source file and appends its symbols to snapshot.
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
		ID:      pkgPath,
		Name:    f.Name.Name,
		Kind:    codeindex.SymbolKindPackage,
		Package: pkgPath,
		File:    rel,
		Line:    fset.Position(f.Package).Line,
		Doc:     firstSentence(docText(f.Doc)),
	}
	snapshot.Symbols = append(snapshot.Symbols, pkgSymbol)

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			snapshot.Symbols = append(snapshot.Symbols, funcSymbol(fset, d, pkgPath, rel))
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					snapshot.Symbols = append(snapshot.Symbols, typeSymbol(fset, d, s, pkgPath, rel))
				case *ast.ValueSpec:
					kind := codeindex.SymbolKindVar
					if d.Tok == token.CONST {
						kind = codeindex.SymbolKindConst
					}
					for _, name := range s.Names {
						snapshot.Symbols = append(snapshot.Symbols, codeindex.Symbol{
							ID:        symbolID(pkgPath, name.Name),
							Name:      name.Name,
							Kind:      kind,
							Package:   pkgPath,
							File:      rel,
							Line:      fset.Position(name.Pos()).Line,
							Signature: fmt.Sprintf("%s %s", name.Name, typeString(fset, s.Type)),
							Doc:       firstSentence(valueSpecDoc(d, s)),
						})
					}
				}
			}
		}
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
		ID:        id,
		Name:      name,
		Kind:      kind,
		Package:   pkgPath,
		File:      rel,
		Line:      fset.Position(d.Pos()).Line,
		Signature: sig,
		Doc:       firstSentence(docText(d.Doc)),
	}
}

func typeSymbol(fset *token.FileSet, gd *ast.GenDecl, s *ast.TypeSpec, pkgPath, rel string) codeindex.Symbol {
	doc := s.Doc
	if doc == nil {
		doc = gd.Doc
	}
	return codeindex.Symbol{
		ID:        symbolID(pkgPath, s.Name.Name),
		Name:      s.Name.Name,
		Kind:      codeindex.SymbolKindType,
		Package:   pkgPath,
		File:      rel,
		Line:      fset.Position(s.Pos()).Line,
		Signature: fmt.Sprintf("type %s %s", s.Name.Name, typeString(fset, s.Type)),
		Doc:       firstSentence(docText(doc)),
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
