// Package jsparser extracts JavaScript/TypeScript class/function/method symbols
// for the code index. It uses lightweight line-based parsing so the agent harness
// stays dependency-free.
package jsparser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lcoder/lcoder/pkg/codeindex"
)

// Indexer parses JS/TS source files into a codeindex.Snapshot.
type Indexer struct {
	exclude  []string
	snapshot *codeindex.Snapshot
	root     string
}

// NewIndexer creates a JS/TS indexer with the given exclude patterns.
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
		if !isJSFile(path) {
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

func isJSFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs":
		return true
	}
	return false
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

var (
	classRE   = regexp.MustCompile(`^(\s*)class\s+([A-Za-z_$][A-Za-z0-9_$]*)(?:\s+extends\s+([A-Za-z_$][A-Za-z0-9_$_.]*))?\s*(?:\{.*)?$`)
	funcRE    = regexp.MustCompile(`^(\s*)(?:export\s+(?:default\s+)?)?(async\s+)?function\s*\*?\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*(\([^)]*\))?(?:\s*:\s*[^ {]+)?\s*\{.*$`)
	methodRE  = regexp.MustCompile(`^(\s*)(?:async\s+)?(?:get\s+|set\s+)?([A-Za-z_$][A-Za-z0-9_$]*)\s*(\([^)]*\))(?:\s*:\s*[^ {]+)?\s*\{.*$`)
	commentRE = regexp.MustCompile(`^\s*(?://\s?(.*)|/\*\s?(.*?)\s?\*/)`)
)

var jsControlKeywords = map[string]bool{
	"if": true, "for": true, "while": true, "switch": true, "catch": true,
	"else": true, "try": true, "finally": true, "with": true,
}

// ParseFile parses a single JS/TS source file and appends its symbols to snapshot.
func (idx *Indexer) ParseFile(snapshot *codeindex.Snapshot, rel, path string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	info, _ := os.Stat(path)
	if info != nil {
		snapshot.Files[rel] = codeindex.FileMeta{
			ModTime: info.ModTime(),
			Size:    info.Size(),
		}
	}

	pkgPath := filepath.Dir(rel)
	if pkgPath == "." {
		pkgPath = ""
	}

	lines := strings.Split(string(src), "\n")

	type scope struct {
		indent    int
		className string
		classID   string
		isClass   bool
	}
	var stack []scope

	for i, line := range lines {
		if m := classRE.FindStringSubmatch(line); m != nil {
			indent := len(m[1])
			for len(stack) > 0 && indent <= stack[len(stack)-1].indent {
				stack = stack[:len(stack)-1]
			}
			name := m[2]
			base := m[3]
			var sig string
			if base == "" {
				sig = fmt.Sprintf("class %s", name)
			} else {
				sig = fmt.Sprintf("class %s extends %s", name, base)
			}
			id := symbolID(pkgPath, name)
			snapshot.Symbols = append(snapshot.Symbols, codeindex.Symbol{
				ID:        id,
				Name:      name,
				Kind:      codeindex.SymbolKindType,
				Package:   pkgPath,
				File:      rel,
				Line:      i + 1,
				Signature: sig,
				Doc:       firstSentence(extractDocComment(lines, i)),
			})
			stack = append(stack, scope{indent: indent, className: name, classID: id, isClass: true})
			continue
		}

		if m := funcRE.FindStringSubmatch(line); m != nil {
			indent := len(m[1])
			for len(stack) > 0 && indent <= stack[len(stack)-1].indent {
				stack = stack[:len(stack)-1]
			}
			async := strings.TrimSpace(m[2])
			name := m[3]
			params := m[4]
			if params == "" {
				params = "()"
			}
			prefix := "function"
			if async != "" {
				prefix = "async function"
			}
			sig := fmt.Sprintf("%s %s%s", prefix, name, params)
			snapshot.Symbols = append(snapshot.Symbols, codeindex.Symbol{
				ID:        symbolID(pkgPath, name),
				Name:      name,
				Kind:      codeindex.SymbolKindFunc,
				Package:   pkgPath,
				File:      rel,
				Line:      i + 1,
				Signature: sig,
				Doc:       firstSentence(extractDocComment(lines, i)),
			})
			continue
		}

		if m := methodRE.FindStringSubmatch(line); m != nil {
			indent := len(m[1])
			for len(stack) > 0 && indent <= stack[len(stack)-1].indent {
				stack = stack[:len(stack)-1]
			}
			name := m[2]
			if jsControlKeywords[name] {
				continue
			}
			if len(stack) == 0 || !stack[len(stack)-1].isClass {
				continue
			}
			params := m[3]
			async := strings.Contains(line, "async ")
			prefix := ""
			if async {
				prefix = "async "
			}
			sig := fmt.Sprintf("%s%s%s", prefix, name, params)
			parent := stack[len(stack)-1]
			snapshot.Symbols = append(snapshot.Symbols, codeindex.Symbol{
				ID:        methodID(pkgPath, parent.className, name),
				Name:      parent.className + "." + name,
				Kind:      codeindex.SymbolKindMethod,
				Package:   pkgPath,
				File:      rel,
				Line:      i + 1,
				Signature: sig,
				Doc:       firstSentence(extractDocComment(lines, i)),
			})
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

func extractDocComment(lines []string, defLineIdx int) string {
	var parts []string
	for i := defLineIdx - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		m := commentRE.FindStringSubmatch(line)
		if m == nil {
			break
		}
		if m[1] != "" {
			parts = append([]string{m[1]}, parts...)
		} else {
			parts = append([]string{m[2]}, parts...)
		}
	}
	return strings.Join(parts, " ")
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
