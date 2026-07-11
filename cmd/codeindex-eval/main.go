// codeindex-eval is a small CLI for evaluating repo-level code indexing retrieval.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/lcoder/lcoder/pkg/codeindex"
	"github.com/lcoder/lcoder/pkg/codeindex/multiparser"
)

func main() {
	languages := flag.String("languages", "go", "comma-separated languages to index")
	flag.Parse()
	args := flag.Args()
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: codeindex-eval [-languages=go,python,javascript,typescript] <root> <query>")
		os.Exit(1)
	}
	root := args[0]
	query := strings.Join(args[1:], " ")

	ctx := context.Background()
	idx := multiparser.NewIndexer(splitLangs(*languages), []string{
		".git/", ".claude/", ".worktrees/", "reference/", "vendor/", "node_modules/", "*_test.go",
	})
	if err := idx.Update(ctx, root); err != nil {
		fmt.Fprintf(os.Stderr, "update index: %v\n", err)
		os.Exit(1)
	}

	results, err := idx.Search(ctx, codeindex.Query{
		Keywords:   split(query),
		MaxResults: 10,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "search: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("root: %s\nquery: %q\nresults: %d\n---\n", root, query, len(results))
	for i, r := range results {
		fmt.Printf("%d. [%s] %s (%.2f)\n%s\n\n", i+1, r.Symbol.Kind, r.Symbol.Name, r.Relevance, r.Stub)
	}
}

func split(s string) []string {
	var out []string
	for _, f := range strings.Fields(s) {
		f = strings.ToLower(strings.TrimSpace(f))
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

func splitLangs(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}
