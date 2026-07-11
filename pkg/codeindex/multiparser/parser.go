// Package multiparser dispatches files to language-specific indexers based on
// extension. It combines all parsed symbols into a single codeindex.Snapshot.
package multiparser

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/lcoder/lcoder/pkg/codeindex"
	"github.com/lcoder/lcoder/pkg/codeindex/goparser"
	"github.com/lcoder/lcoder/pkg/codeindex/jsparser"
	"github.com/lcoder/lcoder/pkg/codeindex/pythonparser"
)

// fileParser is the subset of indexers that multiparser needs.
type fileParser interface {
	ParseFile(snapshot *codeindex.Snapshot, rel, path string) error
}

// Indexer parses a multi-language repository into a single codeindex.Snapshot.
type Indexer struct {
	exclude  []string
	parsers  map[string]fileParser
	snapshot *codeindex.Snapshot
}

// NewIndexer creates a dispatcher that indexes the requested languages.
func NewIndexer(languages, exclude []string) *Indexer {
	parsers := make(map[string]fileParser)
	for _, l := range languages {
		switch strings.ToLower(l) {
		case "go":
			parsers[".go"] = goparser.NewIndexer(exclude)
		case "python", "py":
			parsers[".py"] = pythonparser.NewIndexer(exclude)
			parsers[".pyw"] = pythonparser.NewIndexer(exclude)
		case "javascript", "js", "typescript", "ts":
			parsers[".js"] = jsparser.NewIndexer(exclude)
			parsers[".jsx"] = jsparser.NewIndexer(exclude)
			parsers[".ts"] = jsparser.NewIndexer(exclude)
			parsers[".tsx"] = jsparser.NewIndexer(exclude)
			parsers[".mjs"] = jsparser.NewIndexer(exclude)
			parsers[".cjs"] = jsparser.NewIndexer(exclude)
		}
	}
	return &Indexer{
		exclude: exclude,
		parsers: parsers,
	}
}

// Update performs a full re-parse of root.
func (idx *Indexer) Update(ctx context.Context, root string) error {
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
		if idx.isExcluded(rel) {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		p, ok := idx.parsers[ext]
		if !ok {
			return nil
		}
		if err := p.ParseFile(snapshot, rel, path); err != nil {
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

func (idx *Indexer) Clear() error {
	idx.snapshot = codeindex.NewSnapshot()
	return nil
}

func (idx *Indexer) Search(ctx context.Context, q codeindex.Query) ([]codeindex.Result, error) {
	return codeindex.SearchSnapshot(idx.snapshot, q)
}
