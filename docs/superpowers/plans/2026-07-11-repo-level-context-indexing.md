# Repo-level Context Indexing Engine Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 Lcoder 中实现一个 Go 优先的仓库级代码索引引擎，通过 `repo_index` 工具或可选的自动注入把相关符号 stubs 作为 `BlockRetrieval` 块加入对话上下文，从而降低长上下文 token 消耗并提升召回。

**Architecture:** 新增 `pkg/codeindex` 包，提供语言无关的 `Indexer` 接口；Go 实现放在 `pkg/codeindex/goparser`。索引结果通过 `Injector` 写入 `contextmgr.BlockRetrieval`。`repo_index` 工具由 `pkg/tools/builtin` 提供，并在 `cmd/lcoder/main.go` 的 `prepareAgent` 中完成装配。

**Tech Stack:** Go 标准库 `go/ast`、`go/parser`、`go/token`、`go/format`；复用现有 `pkg/contextmgr`、`pkg/tools`、`pkg/config`。

---

## File Structure

| 文件 | 职责 |
|---|---|
| `pkg/config/config.go` | 新增 `CodeIndexConfig` 类型并在 `Config` 中挂载 |
| `pkg/config/config_validate.go` | 新增 `CodeIndexConfig.Validate` |
| `pkg/codeindex/index.go` | 语言无关类型：`Symbol`、`Relation`、`Query`、`Result`、`Indexer` 接口 |
| `pkg/codeindex/filestore.go` | `Snapshot` 及 JSON 快照读写 |
| `pkg/codeindex/goparser/parser.go` | Go AST 解析、符号提取、搜索、stub 生成 |
| `pkg/codeindex/injector.go` | 把搜索结果注入 `contextmgr` 的 `BlockRetrieval` |
| `pkg/tools/builtin/repo_index.go` | `repo_index` 工具定义与执行 |
| `cmd/lcoder/main.go` | 装配 indexer、injector、工具、可选 auto-inject reminder |
| `configs/lcoder.yaml` | 新增 `code_index` 配置示例 |

---

## Task 1: 新增配置类型与默认值

**Files:**
- Modify: `pkg/config/config.go`
- Test: `pkg/config/config_test.go`

- [ ] **Step 1: 在 `pkg/config/config.go` 末尾追加 `CodeIndexConfig` 类型**

```go
// CodeIndexConfig configures the repository code-indexing engine.
type CodeIndexConfig struct {
    Enabled    bool     `yaml:"enabled"`
    AutoInject bool     `yaml:"auto_inject"`
    MaxResults int      `yaml:"max_results"`
    MaxTokens  int      `yaml:"max_tokens"`
    Languages  []string `yaml:"languages"`
    Exclude    []string `yaml:"exclude"`
}
```

- [ ] **Step 2: 在 `Config` 结构体中加入 `CodeIndex` 字段**

定位 `Config` 结构体（约第 102 行），在 `Memory MemoryConfig` 之后新增：

```go
    CodeIndex CodeIndexConfig `yaml:"code_index"`
```

- [ ] **Step 3: 在 `DefaultConfig()` 中加入默认值**

```go
        CodeIndex: CodeIndexConfig{
            Enabled:    false,
            AutoInject: false,
            MaxResults: 10,
            MaxTokens:  8192,
            Languages:  []string{"go"},
            Exclude:    []string{"vendor/", "**/*_test.go"},
        },
```

- [ ] **Step 4: 编写测试验证默认值**

```go
func TestCodeIndexDefaults(t *testing.T) {
    cfg := DefaultConfig()
    require.False(t, cfg.CodeIndex.Enabled)
    require.Equal(t, 10, cfg.CodeIndex.MaxResults)
    require.Equal(t, 8192, cfg.CodeIndex.MaxTokens)
    require.Equal(t, []string{"go"}, cfg.CodeIndex.Languages)
    require.Equal(t, []string{"vendor/", "**/*_test.go"}, cfg.CodeIndex.Exclude)
}
```

- [ ] **Step 5: 运行测试**

Run: `go test ./pkg/config -run TestCodeIndexDefaults -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/config/config.go pkg/config/config_test.go
git commit -m "feat(config): add CodeIndexConfig with defaults"
```

---

## Task 2: 新增 CodeIndexConfig 校验

**Files:**
- Modify: `pkg/config/config_validate.go`

- [ ] **Step 1: 在 `Config.Validate()` 中调用 `CodeIndex.Validate()`**

在 `Config.Validate()` 中 `c.Memory.Validate()` 之后新增：

```go
    if err := c.CodeIndex.Validate(); err != nil {
        return fmt.Errorf("code_index: %w", err)
    }
```

- [ ] **Step 2: 实现 `CodeIndexConfig.Validate()`**

在 `MemoryConfig.Validate()` 之后新增：

```go
// Validate checks code index settings.
func (c CodeIndexConfig) Validate() error {
    if c.MaxResults < 0 {
        return fmt.Errorf("max_results must be non-negative")
    }
    if c.MaxTokens < 0 {
        return fmt.Errorf("max_tokens must be non-negative")
    }
    return nil
}
```

- [ ] **Step 3: 编写测试**

```go
func TestCodeIndexConfigValidate(t *testing.T) {
    require.NoError(t, CodeIndexConfig{MaxResults: 10, MaxTokens: 1000}.Validate())
    require.Error(t, CodeIndexConfig{MaxResults: -1}.Validate())
    require.Error(t, CodeIndexConfig{MaxTokens: -1}.Validate())
}
```

- [ ] **Step 4: 运行测试**

Run: `go test ./pkg/config -run TestCodeIndexConfigValidate -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/config/config_validate.go pkg/config/config_validate_test.go
git commit -m "feat(config): validate CodeIndexConfig"
```

---

## Task 3: 定义 CodeIndex 核心类型与接口

**Files:**
- Create: `pkg/codeindex/index.go`
- Test: `pkg/codeindex/index_test.go`

- [ ] **Step 1: 创建 `pkg/codeindex/index.go`**

```go
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
```

- [ ] **Step 2: 创建最小测试验证常量**

```go
package codeindex

import (
    "testing"
    "github.com/stretchr/testify/require"
)

func TestSymbolKindValues(t *testing.T) {
    require.Equal(t, SymbolKind("func"), SymbolKindFunc)
    require.Equal(t, SymbolKind("method"), SymbolKindMethod)
}
```

- [ ] **Step 3: 运行测试**

Run: `go test ./pkg/codeindex -run TestSymbolKindValues -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add pkg/codeindex/index.go pkg/codeindex/index_test.go
git commit -m "feat(codeindex): define core types and Indexer interface"
```

---

## Task 4: 实现 JSON 快照存储

**Files:**
- Create: `pkg/codeindex/filestore.go`
- Test: `pkg/codeindex/filestore_test.go`

- [ ] **Step 1: 创建 `pkg/codeindex/filestore.go`**

```go
package codeindex

import (
    "encoding/json"
    "errors"
    "os"
    "path/filepath"
    "time"
)

// SnapshotVersion is the current on-disk index format version.
const SnapshotVersion = "1"

// FileMeta tracks a file's modification state for incremental updates.
type FileMeta struct {
    ModTime time.Time `json:"mod_time"`
    Size    int64     `json:"size"`
}

// Snapshot holds the persisted code index.
type Snapshot struct {
    Version     string              `json:"version"`
    UpdatedAt   time.Time           `json:"updated_at"`
    Files       map[string]FileMeta `json:"files"`
    Symbols     []Symbol            `json:"symbols"`
    Relations   []Relation          `json:"relations"`
    FailedFiles []string            `json:"failed_files"`
}

// NewSnapshot creates an empty snapshot with the current version.
func NewSnapshot() *Snapshot {
    return &Snapshot{
        Version:   SnapshotVersion,
        UpdatedAt: time.Now(),
        Files:     make(map[string]FileMeta),
    }
}

// LoadSnapshot reads a snapshot from disk. Returns an empty snapshot if the file does not exist.
func LoadSnapshot(path string) (*Snapshot, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        if errors.Is(err, os.ErrNotExist) {
            return NewSnapshot(), nil
        }
        return nil, err
    }
    var s Snapshot
    if err := json.Unmarshal(data, &s); err != nil {
        return nil, err
    }
    if s.Files == nil {
        s.Files = make(map[string]FileMeta)
    }
    return &s, nil
}

// SaveSnapshot writes a snapshot to disk atomically.
func SaveSnapshot(path string, s *Snapshot) error {
    if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
        return err
    }
    s.UpdatedAt = time.Now()
    data, err := json.MarshalIndent(s, "", "  ")
    if err != nil {
        return err
    }
    tmp := path + ".tmp"
    if err := os.WriteFile(tmp, data, 0o644); err != nil {
        return err
    }
    return os.Rename(tmp, path)
}
```

- [ ] **Step 2: 编写 round-trip 测试**

```go
package codeindex

import (
    "path/filepath"
    "testing"
    "time"

    "github.com/stretchr/testify/require"
)

func TestSnapshotRoundTrip(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "snapshot.json")

    s := NewSnapshot()
    s.Files["pkg/a.go"] = FileMeta{ModTime: time.Unix(1, 0), Size: 42}
    s.Symbols = []Symbol{{ID: "pkg.A", Name: "A", Kind: SymbolKindType}}
    s.Relations = []Relation{{From: "pkg.A", To: "pkg.B", Kind: "refers"}}
    s.FailedFiles = []string{"bad.go"}

    require.NoError(t, SaveSnapshot(path, s))
    loaded, err := LoadSnapshot(path)
    require.NoError(t, err)
    require.Equal(t, SnapshotVersion, loaded.Version)
    require.Len(t, loaded.Symbols, 1)
    require.Equal(t, "pkg.A", loaded.Symbols[0].ID)
    require.Len(t, loaded.Relations, 1)
}

func TestLoadMissingSnapshot(t *testing.T) {
    s, err := LoadSnapshot(filepath.Join(t.TempDir(), "missing.json"))
    require.NoError(t, err)
    require.Equal(t, SnapshotVersion, s.Version)
    require.Empty(t, s.Symbols)
}
```

- [ ] **Step 3: 运行测试**

Run: `go test ./pkg/codeindex -run 'TestSnapshot|TestLoadMissing' -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add pkg/codeindex/filestore.go pkg/codeindex/filestore_test.go
git commit -m "feat(codeindex): add JSON snapshot store"
```

---

## Task 5: 实现 Go AST 解析器（符号提取）

**Files:**
- Create: `pkg/codeindex/goparser/parser.go`
- Test: `pkg/codeindex/goparser/parser_test.go`

- [ ] **Step 1: 创建 `pkg/codeindex/goparser/parser.go`**

```go
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
```

- [ ] **Step 2: 编写符号提取测试**

```go
package goparser

import (
    "context"
    "os"
    "path/filepath"
    "testing"

    "github.com/lcoder/lcoder/pkg/codeindex"
    "github.com/stretchr/testify/require"
)

func TestParseBasicSymbols(t *testing.T) {
    dir := t.TempDir()
    src := `package demo

// User represents a user.
type User struct {
    Name string
}

// NewUser creates a user.
func NewUser(name string) *User {
    return &User{Name: name}
}

// Greet greets someone.
func (u *User) Greet(msg string) string {
    return msg
}

const MaxCount = 10
var DefaultUser = NewUser("default")
`
    path := filepath.Join(dir, "demo.go")
    require.NoError(t, os.WriteFile(path, []byte(src), 0o644))

    idx := NewIndexer(nil)
    require.NoError(t, idx.Update(context.Background(), dir))

    results, err := idx.Search(context.Background(), codeindex.Query{MaxResults: 20})
    require.NoError(t, err)

    names := make(map[string]codeindex.SymbolKind)
    for _, r := range results {
        names[r.Symbol.Name] = r.Symbol.Kind
    }
    require.Contains(t, names, "demo")
    require.Contains(t, names, "User")
    require.Contains(t, names, "NewUser")
    require.Contains(t, names, "User.Greet")
    require.Contains(t, names, "MaxCount")
    require.Contains(t, names, "DefaultUser")
}
```

- [ ] **Step 3: 运行测试**

Run: `go test ./pkg/codeindex/goparser -run TestParseBasicSymbols -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add pkg/codeindex/goparser/parser.go pkg/codeindex/goparser/parser_test.go
git commit -m "feat(codeindex): parse Go symbols with go/ast"
```

---

## Task 6: 实现搜索、相关性与 Stub 生成

**Files:**
- Modify: `pkg/codeindex/goparser/parser.go`
- Test: `pkg/codeindex/goparser/parser_test.go`

- [ ] **Step 1: 在 `Indexer` 上实现 `Clear()`**

在 `parser.go` 末尾追加：

```go
func (idx *Indexer) Clear() error {
    idx.snapshot = codeindex.NewSnapshot()
    return nil
}
```

- [ ] **Step 2: 实现 `Search`、相关性和 stub 生成**

在 `parser.go` 末尾追加：

```go
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
    for _, sym := range idx.snapshot.Symbols {
        score := scoreSymbol(sym, keywords, q.Symbols)
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
    b.WriteString(fmt.Sprintf("// %s:%d\n%s", sym.File, sym.Line, sym.Signature))
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
```

- [ ] **Step 3: 补充 imports**

确保 `parser.go` 顶部 imports 包含：

```go
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
```

- [ ] **Step 4: 编写搜索测试**

```go
func TestSearchRelevance(t *testing.T) {
    dir := t.TempDir()
    src := `package demo
// Manager manages things.
type Manager struct{}
func NewManager() *Manager { return &Manager{} }
func (m *Manager) Run() {}
`
    require.NoError(t, os.WriteFile(filepath.Join(dir, "demo.go"), []byte(src), 0o644))

    idx := NewIndexer(nil)
    require.NoError(t, idx.Update(context.Background(), dir))

    results, err := idx.Search(context.Background(), codeindex.Query{
        Keywords:   []string{"manager"},
        MaxResults: 5,
    })
    require.NoError(t, err)
    require.NotEmpty(t, results)
    require.Equal(t, "Manager", results[0].Symbol.Name)
    require.Contains(t, results[0].Stub, "type Manager")
}
```

- [ ] **Step 5: 运行测试**

Run: `go test ./pkg/codeindex/goparser -run TestSearchRelevance -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/codeindex/goparser/parser.go pkg/codeindex/goparser/parser_test.go
git commit -m "feat(codeindex): implement search, scoring and stub formatting"
```

---

## Task 7: 实现 Injector

**Files:**
- Create: `pkg/codeindex/injector.go`
- Test: `pkg/codeindex/injector_test.go`

- [ ] **Step 1: 创建 `pkg/codeindex/injector.go`**

```go
package codeindex

import (
    "context"
    "fmt"
    "strings"
    "unicode"

    "github.com/lcoder/lcoder/pkg/contextmgr"
    "github.com/lcoder/lcoder/pkg/models"
)

// Injector writes repo-index stubs into the context manager as a BlockRetrieval block.
type Injector struct {
    indexer   Indexer
    manager   *contextmgr.Manager
    root      string
    maxTokens int
    updated   bool
}

// NewInjector creates an injector bound to a manager and project root.
func NewInjector(idx Indexer, mgr *contextmgr.Manager, root string, maxTokens int) *Injector {
    if maxTokens <= 0 {
        maxTokens = 8192
    }
    return &Injector{
        indexer:   idx,
        manager:   mgr,
        root:      root,
        maxTokens: maxTokens,
    }
}

// Inject searches the index for query and writes matching stubs into context.
func (inj *Injector) Inject(ctx context.Context, query string, maxResults int) error {
    if !inj.updated {
        if err := inj.indexer.Update(ctx, inj.root); err != nil {
            return fmt.Errorf("update code index: %w", err)
        }
        inj.updated = true
    }
    if maxResults <= 0 {
        maxResults = 10
    }
    results, err := inj.indexer.Search(ctx, Query{
        Keywords:   splitQuery(query),
        MaxResults: maxResults,
    })
    if err != nil {
        return err
    }

    var stubs []string
    used := 0
    estimator := inj.manager.Estimator()
    for _, r := range results {
        msg := models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: r.Stub})
        cost := estimator([]models.AgentMessage{msg})
        if used+cost > inj.maxTokens && len(stubs) > 0 {
            break
        }
        stubs = append(stubs, r.Stub)
        used += cost
    }

    block := contextmgr.NewBlockWithCacheHint(
        contextmgr.BlockRetrieval,
        "repo_index",
        contextmgr.StabilityDynamic,
        50,
        contextmgr.CacheHintSkip,
        models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: strings.Join(stubs, "\n\n")}),
    )
    inj.manager.SetBlock(block)
    return nil
}

func splitQuery(s string) []string {
    var out []string
    for _, f := range strings.FieldsFunc(s, func(r rune) bool {
        return unicode.IsSpace(r) || r == '.' || r == '/' || r == '_' || r == '-'
    }) {
        if f != "" {
            out = append(out, strings.ToLower(f))
        }
    }
    return out
}
```

- [ ] **Step 2: 编写注入器测试**

```go
package codeindex

import (
    "context"
    "os"
    "path/filepath"
    "testing"

    "github.com/lcoder/lcoder/pkg/contextmgr"
    "github.com/lcoder/lcoder/pkg/codeindex/goparser"
    "github.com/stretchr/testify/require"
)

func TestInjectorWritesBlock(t *testing.T) {
    dir := t.TempDir()
    src := `package demo
type Engine struct{}
func NewEngine() *Engine { return &Engine{} }
`
    require.NoError(t, os.WriteFile(filepath.Join(dir, "demo.go"), []byte(src), 0o644))

    mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
    idx := goparser.NewIndexer(nil)
    inj := NewInjector(idx, mgr, dir, 2000)

    require.NoError(t, inj.Inject(context.Background(), "engine", 5))

    block, ok := mgr.GetBlock(contextmgr.BlockRetrieval, "repo_index")
    require.True(t, ok)
    text := block.Text()
    require.Contains(t, text, "Engine")
}
```

- [ ] **Step 3: 运行测试**

Run: `go test ./pkg/codeindex -run TestInjectorWritesBlock -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add pkg/codeindex/injector.go pkg/codeindex/injector_test.go
git commit -m "feat(codeindex): add context injector for repo stubs"
```

---

## Task 8: 实现 `repo_index` 工具

**Files:**
- Create: `pkg/tools/builtin/repo_index.go`
- Test: `pkg/tools/builtin/repo_index_test.go`

- [ ] **Step 1: 创建 `pkg/tools/builtin/repo_index.go`**

```go
package builtin

import (
    "context"
    "fmt"

    "github.com/lcoder/lcoder/pkg/codeindex"
    "github.com/lcoder/lcoder/pkg/models"
    "github.com/lcoder/lcoder/pkg/tools"
)

// RepoIndex is a tool that injects repository code stubs into context.
type RepoIndex struct {
    cwd      string
    injector *codeindex.Injector
}

// NewRepoIndex creates the tool. Call SetInjector before Execute.
func NewRepoIndex(cwd string) *RepoIndex {
    return &RepoIndex{cwd: cwd}
}

// SetInjector wires the injector after the context manager is available.
func (r *RepoIndex) SetInjector(inj *codeindex.Injector) {
    r.injector = inj
}

func (r *RepoIndex) Definition() models.ToolDefinition {
    return models.ToolDefinition{
        Name:        "repo_index",
        Description: "Search the repository code index and inject relevant symbol stubs into the context. Use this when you need to understand code relationships without reading whole files.",
        Parameters: map[string]any{
            "type": "object",
            "properties": map[string]any{
                "query": map[string]any{
                    "type":        "string",
                    "description": "Keywords or symbol names to search for",
                },
                "max_results": map[string]any{
                    "type":        "integer",
                    "description": "Maximum number of stubs to inject (default 10)",
                },
            },
            "required": []string{"query"},
        },
    }
}

func (r *RepoIndex) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
    if r.injector == nil {
        return models.ToolExecutionResult{}, fmt.Errorf("repo_index not wired")
    }
    query, _ := args["query"].(string)
    if query == "" {
        return models.ToolExecutionResult{}, fmt.Errorf("missing query")
    }
    maxResults := 10
    if v, ok := args["max_results"].(float64); ok {
        maxResults = int(v)
    } else if v, ok := args["max_results"].(int); ok {
        maxResults = v
    }
    if err := r.injector.Inject(ctx, query, maxResults); err != nil {
        return models.ToolExecutionResult{}, err
    }
    return models.ToolExecutionResult{
        Content: []models.ContentPart{
            models.TextContent{Text: fmt.Sprintf("Repo context injected for query %q", query)},
        },
        Details: map[string]any{"query": query, "max_results": maxResults},
    }, nil
}

var _ tools.Executable = (*RepoIndex)(nil)
```

- [ ] **Step 2: 编写工具测试**

```go
package builtin

import (
    "context"
    "os"
    "path/filepath"
    "testing"

    "github.com/lcoder/lcoder/pkg/codeindex"
    "github.com/lcoder/lcoder/pkg/codeindex/goparser"
    "github.com/lcoder/lcoder/pkg/contextmgr"
    "github.com/stretchr/testify/require"
)

func TestRepoIndexTool(t *testing.T) {
    dir := t.TempDir()
    src := `package demo
type Finder struct{}
func NewFinder() *Finder { return &Finder{} }
`
    require.NoError(t, os.WriteFile(filepath.Join(dir, "demo.go"), []byte(src), 0o644))

    mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
    idx := goparser.NewIndexer(nil)
    inj := codeindex.NewInjector(idx, mgr, dir, 2000)

    tool := NewRepoIndex(dir)
    tool.SetInjector(inj)

    res, err := tool.Execute(context.Background(), "call-1", map[string]any{"query": "finder"})
    require.NoError(t, err)
    require.Contains(t, res.Text(), "Injected")
}
```

- [ ] **Step 3: 运行测试**

Run: `go test ./pkg/tools/builtin -run TestRepoIndexTool -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add pkg/tools/builtin/repo_index.go pkg/tools/builtin/repo_index_test.go
git commit -m "feat(tools): add repo_index tool"
```

---

## Task 9: 在 `prepareAgent` 中装配 CodeIndex

**Files:**
- Modify: `cmd/lcoder/main.go`

- [ ] **Step 1: 在 imports 中加入 goparser 与 codeindex**

```go
    "github.com/lcoder/lcoder/pkg/codeindex"
    "github.com/lcoder/lcoder/pkg/codeindex/goparser"
```

- [ ] **Step 2: 在 `prepareAgent` 中创建并注册 `repo_index` 工具**

定位 `mgr := agentsetup.NewContextManager(...)` 之后、`ag, err := agent.NewBuilder()` 之前，插入：

```go
    var repoIndexTool *builtinTools.RepoIndex
    if cfg.CodeIndex.Enabled {
        indexer := goparser.NewIndexer(cfg.CodeIndex.Exclude)
        injector := codeindex.NewInjector(indexer, mgr, cwd, cfg.CodeIndex.MaxTokens)
        repoIndexTool = builtinTools.NewRepoIndex(cwd)
        repoIndexTool.SetInjector(injector)
        registry.Register("repo_index", repoIndexTool)

        if cfg.CodeIndex.AutoInject {
            reminder := autoInjectReminder(injector)
            // Will be appended to agent config below.
            _ = reminder
        }
    }
```

- [ ] **Step 3: 若开启 deferred tools，确保 `repo_index` 在 core 列表中**

在 `agent.Config{...}` 中 `CoreTools: cfg.Context.CoreTools,` 改为：

```go
            CoreTools: appendCoreTool(cfg.Context.CoreTools, "repo_index"),
```

并在 `cmd/lcoder/main.go` 底部添加辅助函数：

```go
func appendCoreTool(existing []string, name string) []string {
    for _, n := range existing {
        if n == name {
            return existing
        }
    }
    return append(existing, name)
}
```

- [ ] **Step 4: 可选：实现 auto-inject reminder**

在同一文件中添加：

```go
func autoInjectReminder(inj *codeindex.Injector) agent.ReminderProducer {
    return func(msgs []models.AgentMessage) []string {
        for i := len(msgs) - 1; i >= 0; i-- {
            if msgs[i].Role == models.RoleUser {
                query := extractAutoInjectQuery(msgs[i].Text())
                if query == "" {
                    return nil
                }
                ctx := context.Background()
                if err := inj.Inject(ctx, query, 0); err != nil {
                    return nil
                }
                return []string{fmt.Sprintf("[repo_index auto-injected context for: %s]", query)}
            }
        }
        return nil
    }
}

func extractAutoInjectQuery(text string) string {
    text = strings.TrimSpace(text)
    if idx := strings.IndexAny(text, "\n.?!"); idx > 0 {
        text = text[:idx]
    }
    if len(text) > 200 {
        text = text[:200]
    }
    return text
}
```

并在 `agent.Config` 中挂载 reminder：

```go
            ReminderProducers: []agent.ReminderProducer{reminder},
```

注意：若未启用 auto_inject，`reminder` 应为 nil，不设置 `ReminderProducers`。

- [ ] **Step 5: 编译检查**

Run: `go build ./cmd/lcoder`
Expected: 成功编译

- [ ] **Step 6: Commit**

```bash
git add cmd/lcoder/main.go
git commit -m "feat(agent): wire repo_index tool and auto-inject"
```

---

## Task 10: 添加配置示例

**Files:**
- Modify: `configs/lcoder.yaml`

- [ ] **Step 1: 在文件末尾追加 `code_index` 示例**

```yaml
code_index:
  enabled: false
  auto_inject: false
  max_results: 10
  max_tokens: 8192
  languages: ["go"]
  exclude:
    - "vendor/"
    - "**/*_test.go"
```

- [ ] **Step 2: Commit**

```bash
git add configs/lcoder.yaml
git commit -m "docs(config): add code_index example"
```

---

## Task 11: 端到端集成验证

**Files:**
- Test: `pkg/codeindex/integration_test.go`

- [ ] **Step 1: 创建集成测试**

```go
package codeindex_test

import (
    "context"
    "os"
    "path/filepath"
    "testing"

    "github.com/lcoder/lcoder/pkg/codeindex"
    "github.com/lcoder/lcoder/pkg/codeindex/goparser"
    "github.com/lcoder/lcoder/pkg/contextmgr"
    "github.com/stretchr/testify/require"
)

func TestEndToEndRepoContext(t *testing.T) {
    dir := t.TempDir()
    src := `package service
// Service does the work.
type Service struct{}
// NewService constructs a Service.
func NewService() *Service { return &Service{} }
// Run runs the service.
func (s *Service) Run() error { return nil }
`
    require.NoError(t, os.WriteFile(filepath.Join(dir, "service.go"), []byte(src), 0o644))

    mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
    idx := goparser.NewIndexer(nil)
    inj := codeindex.NewInjector(idx, mgr, dir, 4000)

    require.NoError(t, inj.Inject(context.Background(), "service run", 5))

    block, ok := mgr.GetBlock(contextmgr.BlockRetrieval, "repo_index")
    require.True(t, ok)
    text := block.Text()
    require.Contains(t, text, "Service")
    require.Contains(t, text, "NewService")
    require.Contains(t, text, "Run")
}
```

- [ ] **Step 2: 运行全部相关测试**

Run: `go test $(go list ./... | grep -v 'reference/Shannon')`
Expected: PASS（除 reference/Shannon 外）

- [ ] **Step 3: 手动在 Lcoder 自身仓库验证**

Build: `go build -o lcoder ./cmd/lcoder`
Run: `./lcoder -p "用 repo_index 查询 Agent.run 的上下文"`
Expected: 对话中模型调用 `repo_index`，随后 `repo_index` 工具返回成功，后续回答引用到 `Agent.run` 相关 stubs。

- [ ] **Step 4: Commit**

```bash
git add pkg/codeindex/integration_test.go
git commit -m "test(codeindex): add end-to-end integration test"
```

---

## Self-Review Checklist

- [x] Spec coverage：配置、类型、存储、解析器、搜索、injector、工具、装配、测试均有对应任务。
- [x] Placeholder scan：无 `TBD` / `TODO` / `implement later`。
- [x] Type consistency：`CodeIndexConfig`、`Indexer`、`Injector`、`RepoIndex` 签名在计划中保持一致。
- [x] File paths：全部使用绝对路径或仓库相对路径。

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-07-11-repo-level-context-indexing.md`.

Two execution options:

1. **Subagent-Driven (recommended)** - I dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** - Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
