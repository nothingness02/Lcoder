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
	"sort"
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
		if err := idx.parseFile(snapshot, rel, path); err != nil {
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
	for _, p := range idx.exclude {
		if matched, _ := filepath.Match(p, rel); matched {
			return true
		}
		if strings.HasPrefix(rel, p) {
			return true
		}
	}
	return false
}

func (idx *Indexer) parseFile(snapshot *codeindex.Snapshot, rel, path string) error {
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
	sig := fmt.Sprintf("func %s%s", name, typeString(fset, d.Type))
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
	if idx.snapshot == nil {
		return nil, nil
	}
	max := q.MaxResults
	if max <= 0 {
		max = 10
	}
	keywords := normalize(q.Keywords)
	var results []codeindex.Result
	emptyQuery := len(keywords) == 0 && len(q.Symbols) == 0
	for _, sym := range idx.snapshot.Symbols {
		score := scoreSymbol(sym, keywords, q.Symbols)
		if emptyQuery {
			score = 1.0
		}
		if score <= 0 {
			continue
		}
		results = append(results, codeindex.Result{
			Symbol:    sym,
			Relevance: score,
			Stub:      idx.formatStub(sym),
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

func normalize(words []string) []string {
	var out []string
	for _, w := range words {
		w = strings.ToLower(strings.TrimSpace(w))
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

func scoreSymbol(sym codeindex.Symbol, keywords []string, exact []string) float64 {
	score := 0.0
	text := strings.ToLower(sym.Name + " " + sym.Package + " " + sym.Doc + " " + sym.Signature)
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

func (idx *Indexer) formatStub(sym codeindex.Symbol) string {
	var b strings.Builder
	fmt.Fprintf(&b, "// %s:%d\n%s", sym.File, sym.Line, sym.Signature)
	related := idx.relatedSymbols(sym.ID, 3)
	if len(related) > 0 {
		b.WriteString("\n// Related: " + strings.Join(related, ", "))
	}
	return b.String()
}

func (idx *Indexer) relatedSymbols(id string, max int) []string {
	if idx.snapshot == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range idx.snapshot.Relations {
		if r.From == id && !seen[r.To] {
			seen[r.To] = true
			out = append(out, r.To)
		}
		if r.To == id && !seen[r.From] {
			seen[r.From] = true
			out = append(out, r.From)
		}
		if len(out) >= max {
			break
		}
	}
	return out
}
