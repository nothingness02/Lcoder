# Repo-level Context Indexing Engine 设计文档

- **日期**: 2026-07-11
- **方向**: 基于语言特征的代码库上下文索引引擎
- **路线**: 混合渐进式（Go / `go/ast` 优先，接口可替换为 Tree-sitter）
- **优先级**: P1（对应目标 B：长上下文 token 效率与响应质量）

## 1. 概述

Lcoder 当前的 `contextmgr` 已经支持分块预算、压缩和缓存断点，但对话中注入的代码上下文仍依赖模型主动调用 `read`/`grep`/`ls` 等工具。长上下文场景下，这会导致：

1. 反复读取完整文件，浪费 token。
2. 模型难以 recall 跨文件的真正相关符号。

本设计引入一个轻量的 **Repo-level Context Indexing Engine**，在工作树上构建“包 → 类型 → 方法 → 引用”的符号图，并在 turn 请求中把高相关度的 **方法定义与上下文桩（stubs）** 以 `BlockRetrieval` 块的形式注入。

## 2. 目标与非目标

### 2.1 目标（MVP）

- 仅支持 Go，使用标准库 `go/ast` + `go/parser`。
- 按需索引工作树：首次调用 `repo_index` 时构建；若开启 `auto_inject`，则在首个 turn 触发构建。
- 将解析结果缓存到 `~/.lcoder/index/<repo-hash>/goindex.json`，其中 `repo-hash` 由项目根目录绝对路径的稳定哈希生成。
- 提供 `repo_index` 工具，让模型显式查询；同时支持可选的自动注入。
- 生成仅含签名和简短文档的 stubs，不包含函数体。

### 2.2 非目标

- 多语言支持（预留接口，后续可接入 Tree-sitter）。
- 深度类型检查或 100% 精确的调用图。
- 向量 / 语义检索。
- 直接替代 `read`/`edit`/`grep` 工具；stubs 只是补充提示。

## 3. 现有架构适配点

- `pkg/contextmgr/block.go` 已定义 `BlockRetrieval`，专门用于 RAG / code index 结果，天然适合承载本功能。
- `pkg/contextmgr/manager.go` 的 `BuildTurnRequest` 会按 `DefaultBlockOrder` 排列 blocks；`BlockRetrieval` 已位于 `BlockSummary` 之后、`BlockRecent` 之前。
- `pkg/agent/executor.go` 支持本地解析 `tool_search` 等元工具；新增 `repo_index` 工具可沿用同一模式。
- `pkg/config/config.go` 与 `configs/lcoder.yaml` 需要新增 `code_index` 配置段。

## 4. 架构与组件

### 4.1 新增文件/包

```text
pkg/codeindex/
├── index.go              # 核心类型与 Indexer 接口
├── filestore.go          # JSON 快照缓存
├── injector.go           # 将检索结果注入 contextmgr
└── goparser/
    ├── parser.go         # Go AST 实现
    └── parser_test.go    # 单元测试

pkg/tools/builtin/
└── repo_index.go         # repo_index 工具；若启用 deferred tool loading，应将其加入 core_tools 或在 tool_search 中可被提升

pkg/config/config.go      # 新增 CodeIndexConfig
configs/lcoder.yaml       # 新增 code_index 配置示例
```

### 4.2 核心接口

```go
package codeindex

type Indexer interface {
    Update(ctx context.Context, root string) error
    Search(ctx context.Context, q Query) ([]Result, error)
    Clear() error
}

type SymbolKind string

const (
    SymbolKindPackage SymbolKind = "package"
    SymbolKindType    SymbolKind = "type"
    SymbolKindFunc    SymbolKind = "func"
    SymbolKindMethod  SymbolKind = "method"
    SymbolKindVar     SymbolKind = "var"
    SymbolKindConst   SymbolKind = "const"
)

type Symbol struct {
    ID        string    // 唯一标识，如 "github.com/lcoder/lcoder/pkg/agent.Agent"
    Name      string
    Kind      SymbolKind
    Package   string
    File      string
    Line      int
    Signature string    // 函数/方法签名或类型定义头
    Doc       string    // 文档注释第一句
}

type Relation struct {
    From string // Symbol ID
    To   string // Symbol ID
    Kind string // "calls", "refers", "implements", "embeds"
}

type Query struct {
    Keywords     []string
    Symbols      []string   // 精确 symbol ID / 名称
    Kinds        []SymbolKind
    MaxResults   int
    IncludeTests bool
}

type Result struct {
    Symbol    Symbol
    Relevance float64
    Stub      string // 已格式化的上下文桩
}
```

### 4.3 Go AST 实现（`goparser.Indexer`）

- 扫描 `.go` 文件，默认排除 `vendor/` 和测试文件（可配置）。
- 用 `go/parser.ParseFile`（`parser.ParseComments`）解析每个文件。
- 提取：
  - 包声明与导入。
  - `type`、`func`、`method`、`const`、`var`。
  - 每个符号的第一行文档注释作为 `Doc`。
- 引用关系采用 AST 级启发式：
  - 识别 `sel.X` 形式的选择器。
  - 对同包内标识符做简单名称匹配。
  - 不保证 100% 精确，仅用于相关性排序。

### 4.4 缓存存储（`filestore`）

- 快照结构：
  ```go
  type Snapshot struct {
      Version    string
      UpdatedAt  time.Time
      Files      map[string]FileMeta // path -> mtime/size
      Symbols    []Symbol
      Relations  []Relation
      FailedFiles []string
  }
  ```
- `Update` 时：
  1. 加载已有快照（若存在）。
  2. 遍历工作树，对比 `mtime`/`size` 决定重解析、删除或新增。
  3. 解析后写入 `goindex.json`。

### 4.5 注入器（`Injector`）

```go
type Injector struct {
    indexer   codeindex.Indexer
    manager   *contextmgr.Manager
    maxTokens int
}

func (i *Injector) Inject(ctx context.Context, query string) error
```

- 调用 `indexer.Search` 获取 `Result` 列表。
- 按 `Relevance` 排序，逐个累加 stub token 数，直到接近 `code_index.max_tokens`。
- 用 `contextmgr.NewBlock(BlockRetrieval, "repo_index", StabilityDynamic, 50, ...)` 写入 manager。
- 设置 `CacheHintSkip`，因为内容每 turn 都会变化。

### 4.6 `repo_index` 工具

```go
func (r *RepoIndex) Definition() models.ToolDefinition {
    return models.ToolDefinition{
        Name:        "repo_index",
        Description: "Search the repository code index and inject relevant symbol stubs into the context.",
        Parameters: map[string]any{
            "type": "object",
            "properties": map[string]any{
                "query": map[string]any{
                    "type":        "string",
                    "description": "Keywords or symbol names to search for",
                },
                "max_results": map[string]any{
                    "type":        "integer",
                    "description": "Maximum number of stubs to inject",
                },
            },
            "required": []string{"query"},
        },
    }
}
```

工具执行后返回注入的 stubs 摘要，不直接返回完整内容（内容已进 context）。

## 5. 数据流

1. **Agent 启动**：`prepareAgent` 读取 `code_index.enabled`；若为 true，则创建 `goparser.Indexer` 与 `Injector`，并注册 `repo_index` 工具。
2. **用户提问 / 模型调用**：
   - 显式：模型调用 `repo_index{"query": "Agent run loop"}`。
   - 自动：若 `auto_inject: true`，`ReminderProducer` 从最近用户消息和任务列表提取关键词，调用 `Injector.Inject`。
3. **索引更新**：首次搜索时触发 `Indexer.Update(root)`，后续按文件变更增量更新。
4. **上下文注入**：`Injector` 将 stubs 写入 `contextmgr` 的 `BlockRetrieval`。
5. **Turn 请求**：`BuildTurnRequest` 把 `BlockRetrieval` 与其他 blocks 一起按预算裁剪后发给 LLM。

## 6. Stub 格式

### 6.1 函数 / 方法

```go
// pkg/agent/loop.go:378
func (a *Agent) run(ctx context.Context, initialPrompts []models.AgentMessage) error
// Related: Agent.Prompt, Agent.Continue, contextmgr.Manager
```

### 6.2 类型

```go
// pkg/contextmgr/manager.go:90
type Manager struct { ... } // fields omitted
func NewManager(budget TokenBudget, opts ...Option) *Manager
```

### 6.3 包

```go
// package pkg/tools
// Tool registry, deferred loading, and built-in tool wiring.
```

## 7. 错误处理

| 场景 | 行为 |
|---|---|
| 单个文件解析失败 | 记录到 `FailedFiles`，继续索引其余文件；下次 `Update` 仅在文件变更时重试 |
| 索引文件缺失 | 首次搜索时自动全量构建 |
| 缓存损坏/JSON 解析失败 | 清空缓存并重建 |
| 搜索无结果 | 返回空 `BlockRetrieval` 和提示信息，不中断对话 |
| stubs 超过预算 | 按 relevance 截断，至少保留 Top-1 |

## 8. 配置

```yaml
code_index:
  enabled: true
  auto_inject: false        # 是否自动从用户消息提取关键词注入
  max_results: 10           # repo_index 默认最大返回数
  max_tokens: 8192          # BlockRetrieval 块的 token 上限
  languages: ["go"]         # 未来可扩展
  exclude:                  # 排除模式
    - "vendor/"
    - "**/*_test.go"
```

## 9. 测试策略

- `pkg/codeindex/goparser/parser_test.go`：使用 fixture Go 文件验证符号、引用、Doc 提取正确性。
- `pkg/codeindex/filestore_test.go`：验证快照读写、增量更新、损坏重建。
- `pkg/codeindex/injector_test.go`：验证 token 预算截断和 block 注入。
- `pkg/tools/builtin/repo_index_test.go`：验证工具参数解析与执行。
- 集成验证：在 Lcoder 自身代码库上调用 `repo_index`，检查是否返回 `Agent.run`、`Manager.BuildTurnRequest` 等预期符号。
- 运行命令沿用现有：
  ```bash
  go test $(go list ./... | grep -v 'reference/Shannon')
  ```

## 10. 风险与缓解

| 风险 | 缓解 |
|---|---|
| AST 级引用不精确 | 仅作为相关性排序依据，不替代 `read`/`grep`；结果保留 fallback |
| 大仓库首次索引慢 | 支持增量更新；后续可按“仅当前包 + 直接依赖包”进一步限制 |
| 模型过度依赖 stubs | 明确 prompt 说明 stubs 仅供参考，关键代码仍需 `read` |
| 多语言需求提前出现 | `Indexer` 接口已抽象，后续可在 `pkg/codeindex/treesitter` 实现 |

## 11. 后续演进

1. **P1.5**：把 `auto_inject` 的 keyword 提取做得更稳（基于当前任务列表和最近的 user message）。
2. **P2**：接入 Tree-sitter 后端，支持 Python / TypeScript 等，替换或并列于 `goparser`。
3. **P3**：引入轻量调用图分析（如 `golang.org/x/tools/go/packages`）以提升引用精度。

## 12. 参考

- `pkg/contextmgr/manager.go`：`BuildTurnRequest`、block 排序与预算逻辑。
- `pkg/contextmgr/block.go`：`BlockRetrieval` 块定义。
- `pkg/agent/executor.go`：工具执行与本地元工具（`tool_search`）处理模式。
- `pkg/tools/builtin/edit.go` / `write.go`：现有文件修改工具，stubs 不替代它们。
