// Command codeindex-eval measures basic metrics for the code index.
//
// It reports full/incremental index latency, database size, graph scale, and
// simple symbol recall/precision against the Go source files in the target root.
package main

import (
	"context"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lcoder/lcoder/pkg/codeindex"
	"github.com/lcoder/lcoder/pkg/codeindex/sqlitestore"
	"github.com/lcoder/lcoder/pkg/config"
)

func main() {
	var (
		root    = flag.String("root", ".", "Project root to index")
		dbPath  = flag.String("db", "", "SQLite database path (default: temp file)")
		queries = flag.String("queries", "Update,Search,Agent,Node,BuildTurnRequest", "Comma-separated search queries")
	)
	flag.Parse()

	cfg := config.DefaultConfig().CodeIndex
	if *dbPath == "" {
		f, err := os.CreateTemp("", "codeindex-eval-*.db")
		if err != nil {
			fmt.Fprintf(os.Stderr, "create temp db: %v\n", err)
			os.Exit(1)
		}
		*dbPath = f.Name()
		_ = f.Close()
		defer os.Remove(*dbPath)
	}

	absRoot, err := filepath.Abs(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve root: %v\n", err)
		os.Exit(1)
	}

	idx, err := sqlitestore.NewIndexer(cfg.Languages, cfg.Exclude, *dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create indexer: %v\n", err)
		os.Exit(1)
	}
	defer idx.Close()

	ctx := context.Background()

	fileCount := countSourceFiles(absRoot, cfg.Exclude, cfg.Languages)

	fmt.Println("=== Code Index Metrics ===")
	fmt.Printf("Root:        %s\n", absRoot)
	fmt.Printf("DB path:     %s\n", *dbPath)
	fmt.Printf("Languages:   %v\n", cfg.Languages)
	fmt.Printf("Source files: %d\n", fileCount)

	// Full index.
	start := time.Now()
	if err := idx.Update(ctx, absRoot); err != nil {
		fmt.Fprintf(os.Stderr, "full index: %v\n", err)
		os.Exit(1)
	}
	fullDur := time.Since(start)

	files, nodes, edges, unresolved, err := idx.Stats(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stats: %v\n", err)
		os.Exit(1)
	}
	dbSize := dbSizeBytes(*dbPath)

	fmt.Printf("\nFull index:   %s\n", fullDur)
	fmt.Printf("DB size:      %s (%d bytes)\n", humanBytes(dbSize), dbSize)
	fmt.Printf("Files:        %d\n", files)
	fmt.Printf("Nodes:        %d\n", nodes)
	fmt.Printf("Edges:        %d\n", edges)
	fmt.Printf("Unresolved:   %d\n", unresolved)

	// Incremental index (full tree walk).
	tmpFile := filepath.Join(absRoot, "_codeindex_eval_tmp.go")
	_ = os.WriteFile(tmpFile, []byte("package eval\nfunc EvalTemp() {}\n"), 0o644)
	start = time.Now()
	if err := idx.Update(ctx, absRoot); err != nil {
		fmt.Fprintf(os.Stderr, "incremental index: %v\n", err)
		os.Exit(1)
	}
	incDur := time.Since(start)
	_ = os.Remove(tmpFile)
	fmt.Printf("Incremental (walk):  %s\n", incDur)

	// Incremental index (targeted file update, as used by the watcher).
	tmpFile2 := filepath.Join(absRoot, "_codeindex_eval_tmp2.go")
	_ = os.WriteFile(tmpFile2, []byte("package eval\nfunc EvalTemp2() {}\n"), 0o644)
	start = time.Now()
	if err := idx.UpdateFiles(ctx, absRoot, []string{"_codeindex_eval_tmp2.go"}); err != nil {
		fmt.Fprintf(os.Stderr, "targeted incremental index: %v\n", err)
		os.Exit(1)
	}
	incTargetedDur := time.Since(start)
	_ = os.Remove(tmpFile2)
	fmt.Printf("Incremental (targeted): %s\n", incTargetedDur)

	// Symbol recall/precision against Go declarations.
	expected := goDeclarationNames(absRoot, cfg.Exclude)
	indexed := indexedSymbolNames(ctx, idx)
	overlap := intersect(expected, indexed)
	recall := 0.0
	precision := 0.0
	if len(expected) > 0 {
		recall = float64(len(overlap)) / float64(len(expected))
	}
	if len(indexed) > 0 {
		precision = float64(len(overlap)) / float64(len(indexed))
	}
	fmt.Printf("\nSymbol recall:    %.2f%% (%d/%d)\n", recall*100, len(overlap), len(expected))
	fmt.Printf("Symbol precision: %.2f%% (%d/%d)\n", precision*100, len(overlap), len(indexed))

	// Search latency and recall.
	fmt.Println("\nSearch queries:")
	qList := strings.Split(*queries, ",")
	totalLatency := time.Duration(0)
	hits := 0
	for _, q := range qList {
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}
		start := time.Now()
		res, err := idx.Search(ctx, codeindex.Query{Keywords: []string{q}, MaxResults: 5})
		latency := time.Since(start)
		if err != nil {
			fmt.Printf("  %q: error %v\n", q, err)
			continue
		}
		totalLatency += latency
		found := false
		for _, r := range res {
			if strings.EqualFold(r.Node.Name, q) || strings.EqualFold(r.Node.QualifiedName, q) {
				found = true
				break
			}
			// Method names are indexed as "Receiver.Name"; count a suffix match
			// as a hit for keyword search.
			if strings.HasSuffix(strings.ToLower(r.Node.Name), "."+strings.ToLower(q)) {
				found = true
				break
			}
		}
		if found {
			hits++
		}
		fmt.Printf("  %q: %d results, latency=%s, hit=%v\n", q, len(res), latency, found)
	}
	if len(qList) > 0 {
		fmt.Printf("Avg latency: %s\n", totalLatency/time.Duration(len(qList)))
		fmt.Printf("Top-5 recall: %.2f%% (%d/%d)\n", float64(hits)/float64(len(qList))*100, hits, len(qList))
	}
}

func countSourceFiles(root string, exclude, languages []string) int {
	exts := make(map[string]bool)
	for _, l := range languages {
		exts["."+strings.ToLower(l)] = true
	}
	count := 0
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if isExcluded(rel, exclude) {
				return filepath.SkipDir
			}
			return nil
		}
		if isExcluded(rel, exclude) {
			return nil
		}
		if exts[strings.ToLower(filepath.Ext(path))] {
			count++
		}
		return nil
	})
	return count
}

func goDeclarationNames(root string, exclude []string) map[string]struct{} {
	names := make(map[string]struct{})
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if isExcluded(rel, exclude) {
				return filepath.SkipDir
			}
			return nil
		}
		if isExcluded(rel, exclude) || filepath.Ext(path) != ".go" {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					names[d.Name.Name] = struct{}{}
				} else {
					// Include method names but not receiver-qualified.
					names[d.Name.Name] = struct{}{}
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						names[s.Name.Name] = struct{}{}
					case *ast.ValueSpec:
						for _, id := range s.Names {
							names[id.Name] = struct{}{}
						}
					}
				}
			}
		}
		return nil
	})
	return names
}

func indexedSymbolNames(ctx context.Context, idx *sqlitestore.Indexer) map[string]struct{} {
	nodes, err := idx.AllNodes(ctx)
	if err != nil {
		return nil
	}
	names := make(map[string]struct{})
	for _, n := range nodes {
		if n.Name == "" || n.Kind == codeindex.NodeKindPackage {
			continue
		}
		names[n.Name] = struct{}{}
	}
	return names
}

func intersect(a, b map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{})
	for k := range a {
		if _, ok := b[k]; ok {
			out[k] = struct{}{}
		}
	}
	return out
}

func dbSizeBytes(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n >= div*unit && exp < 3 {
		div *= unit
		exp++
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	return fmt.Sprintf("%.1f %s", float64(n)/float64(div), units[exp])
}

func isExcluded(rel string, exclude []string) bool {
	rel = filepath.ToSlash(rel)
	for _, p := range exclude {
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
