// Package pythonparser extracts Python class/function/method symbols for the code
// index. It uses lightweight line-based parsing rather than a full Python AST so
// the agent harness stays dependency-free.
package pythonparser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lcoder/lcoder/pkg/codeindex"
)

// Indexer parses Python source files into a codeindex.Snapshot.
type Indexer struct {
	exclude  []string
	snapshot *codeindex.Snapshot
	root     string
}

// NewIndexer creates a Python indexer with the given exclude patterns.
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
		if !isPythonFile(path) {
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

func isPythonFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".py" || ext == ".pyw"
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

var pyDefRE = regexp.MustCompile(`^(\s*)(?:(async)\s+)?(class|def)\s+([A-Za-z_][A-Za-z0-9_]*)\s*(.*)$`)
var pyStringRE = regexp.MustCompile(`^\s*("""[\s\S]*?"""|'''[\s\S]*?'''|"[^"]*"|'[^']*')`)

// ParseFile parses a single Python source file and appends its symbols to snapshot.
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
		m := pyDefRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		indent := len(m[1])
		async := m[2]
		keyword := m[3]
		name := m[4]
		rest := strings.TrimSpace(m[5])
		// Strip trailing colon from the signature fragment.
		rest = strings.TrimSuffix(rest, ":")
		rest = strings.TrimSpace(rest)

		for len(stack) > 0 && indent <= stack[len(stack)-1].indent {
			stack = stack[:len(stack)-1]
		}

		lineNo := i + 1
		var kind codeindex.SymbolKind
		var sig string
		var id, displayName string

		switch keyword {
		case "class":
			kind = codeindex.SymbolKindType
			if rest == "" {
				sig = fmt.Sprintf("class %s", name)
			} else if strings.HasPrefix(rest, "(") {
				sig = fmt.Sprintf("class %s%s", name, rest)
			} else {
				sig = fmt.Sprintf("class %s(%s)", name, rest)
			}
			id = symbolID(pkgPath, name)
			displayName = name
			stack = append(stack, scope{indent: indent, className: name, classID: id, isClass: true})
		case "def":
			prefix := "def"
			if async != "" {
				prefix = "async def"
			}
			if rest == "" {
				sig = fmt.Sprintf("%s %s()", prefix, name)
			} else if strings.HasPrefix(rest, "(") {
				sig = fmt.Sprintf("%s %s%s", prefix, name, rest)
			} else {
				sig = fmt.Sprintf("%s %s(%s)", prefix, name, rest)
			}
			if len(stack) > 0 && stack[len(stack)-1].isClass {
				kind = codeindex.SymbolKindMethod
				parent := stack[len(stack)-1]
				id = methodID(pkgPath, parent.className, name)
				displayName = parent.className + "." + name
			} else {
				kind = codeindex.SymbolKindFunc
				id = symbolID(pkgPath, name)
				displayName = name
			}
		}

		doc := firstSentence(extractDocstring(lines, i))
		snapshot.Symbols = append(snapshot.Symbols, codeindex.Symbol{
			ID:        id,
			Name:      displayName,
			Kind:      kind,
			Package:   pkgPath,
			File:      rel,
			Line:      lineNo,
			Signature: sig,
			Doc:       doc,
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

func extractDocstring(lines []string, defLineIdx int) string {
	for i := defLineIdx + 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m := pyStringRE.FindStringSubmatch(line)
		if m == nil {
			return ""
		}
		s := m[1]
		// Strip quote delimiters.
		for _, q := range []string{"\"\"\"", "'''", "\"", "'"} {
			if strings.HasPrefix(s, q) && strings.HasSuffix(s, q) {
				return strings.TrimSpace(s[len(q) : len(s)-len(q)])
			}
		}
		return strings.TrimSpace(s)
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
