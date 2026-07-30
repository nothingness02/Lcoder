# 工具执行并发优化

> 状态: 已实施(2026-07-30) | 参考: Kimi Code `ToolAccesses` + `ToolScheduler`
>
> 实施注记: `ExecutionMode` 标签已完全退役(含 agent 级 `ToolExecutionMode`、
> HTTP 工具 `execution_mode` 配置、checkpoint 字段);`switch_mode` 的整批串行
> veto 一并移除——mode 的消费(守卫确认)全部在串行 prepare 阶段完成,run 阶段
> 没有 mode 读者。同批次重复读通过调度器 `addWait` 边保证 dedup 确定性命中。

## 现状

Lcoder 使用**工具级别**的执行模式标签：

```go
type ExecutionMode string
const (
    ExecutionParallel   = "parallel"    // 可并发
    ExecutionSequential = "sequential"  // 必须串行
)
```

### 判定逻辑（`executor.go`）

```
如果批次中任意一个工具标记为 sequential → 整个批次串行
否则 → 并发
```

### 问题

同一个文件中 read 和 edit 确实不能并发（有读写冲突），但**不同文件**的 edit 可以并发。当前设计做不到这一点：

```
批次: [edit a.go, edit b.go]
  当前: 串行（因为 edit 标记为 sequential）
  理想: 并发（a.go 和 b.go 无冲突）
```

```
批次: [read a.go, edit a.go]
  当前: 串行（edit 标记为 sequential）
  理想: 串行（同一文件有冲突） ← 碰巧正确
```

第一行是过度保守，第二行是碰巧正确。两者都缺乏细粒度区分。

## 参考实现（已核对源码）

### Kimi Code（v1 与 v2 相同）

v1: `agent-core/src/loop/tool-access.ts` + `tool-scheduler.ts`；v2 重写后 `agent-core-v2/src/agent/toolExecutor/toolScheduler.ts` 与 v1 逐行一致，说明该设计经受住了重写考验。

每个工具通过 `resolveExecution(args)` 声明访问的资源路径：

```typescript
// Read 工具
resolveExecution(args) {
    return {
        accesses: ToolAccesses.readFile(path),   // {kind:'file', operation:'read', path:'a.go'}
        execute: () => this.execution(args, path),
    }
}

// Edit 工具
resolveExecution(args) {
    return {
        accesses: ToolAccesses.readWriteFile(path), // {kind:'file', operation:'readwrite', path:'b.go'}
        execute: () => this.execution(args, path),
    }
}
```

冲突判定规则（`tool-access.ts`，逐行核对）：

```
两个 access 冲突，当：
  1. 至少有一个是写操作（write / readwrite）
  2. 且路径重叠（同文件，或一个 recursive 目录包含另一个的路径）

read/search + read/search → 不冲突（都不写）
read + write              → 冲突（同一路径）
edit a.go + edit b.go     → 不冲突（不同文件）
bash                      → kind:'all' → 和所有工具冲突
```

路径比较前必须先规范化（`normalizePath`）：反斜杠→斜杠、折叠重复斜杠、
**整体小写化**（Windows/macOS 大小写不敏感）、去尾部斜杠。漏掉小写化会在
Windows 上把同一文件误判为不冲突。

调度器语义（`tool-scheduler.ts`）：

- `add(task)`：与 **active 或排在前面的 queued** 任一冲突 → 入队等待；否则立即启动。
  与 queued 比较保证了 FIFO 公平：后来的无冲突任务不能插队到有冲突任务前面。
- 任务完成后按序重扫队列，启动所有变为不冲突的任务。
- 结果按 provider 顺序 drain，完成顺序可以乱。

两阶段结构（`tool-call.ts`）：`prepareToolCall`（参数校验 + 路径安全 + 权限确认）
**按 provider 顺序串行**执行，产出 `{accesses, start}`；调度器只管执行重叠。
交互式权限确认因此不会并发交错。

### pi（更简备选）

`coding-agent/src/core/tools/file-mutation-queue.ts`：无调度器，全部并行启动，
仅 write/edit 在全局 per-file 互斥队列（realpath 作 key）上串行化。只解决同文件
写冲突，不处理 read-vs-write 时序，也不保留 FIFO。

### opencode（反例）

`session/processor.ts` 中工具调用基本串行执行，未做并发优化。

### Lcoder 已有基础

- `executor.go` 的 `pathOpForTool` 已按工具名标注 read/write/search（当前仅用于
  路径安全守卫的错误消息），访问声明可与此映射合并，一处标注两处使用。
- `builtin.ResolvePathAccess` 已做路径解析与校验，可作为 access 路径的解析入口。

## 改造方案

### 新增：ToolAccess 接口

```go
// pkg/tools/access.go

type FileOperation string
const (
    OpRead      FileOperation = "read"
    OpWrite     FileOperation = "write"
    OpReadWrite FileOperation = "readwrite"
    OpSearch    FileOperation = "search"
    OpAll       FileOperation = "all"
)

type ToolAccess struct {
    Kind      FileOperation
    Path      string
    Recursive bool
}

func AccessConflict(a, b ToolAccess) bool {
    if a.Kind == OpAll || b.Kind == OpAll {
        return true
    }
    if writes(a.Kind) || writes(b.Kind) { // read/search 不写
        return pathOverlaps(a, b) // 需先 normalizePath：\ → /、折叠 //、小写化、去尾 /
    }
    return false
}
```

### 调度器（Go 适配）

Go 没有 promise 着色问题，且整个批次在执行前已知，调度比 Kimi 更简单：
按 provider 顺序启动，每个调用等待"所有排在自己前面且冲突的调用"完成即可，
天然等价于 Kimi 的 active+queued FIFO 语义：

```go
// pkg/agent/scheduler.go（批次内一次性使用，非长生命周期组件）
type batchScheduler struct {
    mu       sync.Mutex
    running  map[int][]tools.ToolAccess // index → accesses（未完成的）
    done     chan struct{}              // 每次完成广播
}

// wait 阻塞到所有 index < i 且与 accesses 冲突的调用完成。
func (s *batchScheduler) wait(i int, accesses []tools.ToolAccess)
func (s *batchScheduler) finish(i int)
```

### 两阶段拆分

`executeOneToolCall` 拆为 prepare（校验 + 路径守卫 + 权限确认 + before hook，
按 provider 顺序串行）与 run（registry.Execute + after hook + 事件，可并发）。
这样交互式 Ask 确认不会并发交错——这是采用两阶段的直接动机，而非照搬 Kimi。

### 修改：Executable 接口

```go
// 新增可选接口
type AccessDeclarer interface {
    DeclareAccesses(args map[string]any) []ToolAccess
}

// executor 中:
if declarer, ok := exec.(AccessDeclarer); ok {
    accesses := declarer.DeclareAccesses(args)
    // 用 ToolScheduler 调度并发 vs 串行
}
```

### 工具改造示例

```go
// edit.go - 声明读写 a.go
func (e *Edit) DeclareAccesses(args map[string]any) []ToolAccess {
    path, _ := tools.RequiredString(args, "path")
    return []ToolAccess{{Kind: OpReadWrite, Path: path}}
}

// read.go - 声明只读 a.go
func (r *Read) DeclareAccesses(args map[string]any) []ToolAccess {
    path, _ := tools.RequiredString(args, "path")
    return []ToolAccess{{Kind: OpRead, Path: path}}
}

// bash.go - 可能有任何副作用
func (b *Bash) DeclareAccesses(args map[string]any) []ToolAccess {
    return []ToolAccess{{Kind: OpAll}}
}

// grep.go / find.go - 递归搜索，只读但与写冲突时需按路径判定
func (g *Grep) DeclareAccesses(args map[string]any) []ToolAccess {
    path, _ := tools.RequiredString(args, "path")
    return []ToolAccess{{Kind: OpSearch, Path: path, Recursive: true}}
}
```

未实现 `AccessDeclarer` 的工具（HTTP/MCP/未来扩展）保守视为 `OpAll`（串行），
替代现有的 `ExecutionMode` 静态标签；标签可在迁移完成后退役。

## 影响评估

| 维度 | 影响 |
|------|------|
| 正确性 | 无影响。当前串行是正确子集 |
| 性能 | 多文件 edit 场景可提速（LLM 很少这样用） |
| 接口兼容 | 需新增 `AccessDeclarer` 可选接口 |
| 改动量 | ~250 行（access.go + 批次调度器 + executeOneToolCall 两阶段拆分 + 工具适配） |
| 风险 | 低。可渐进迁移，未实现接口的工具保持串行 |

## 待决策

- [x] 是否值得做？→ **做，按 Kimi 语义**。场景不止多文件 edit：read+grep+find 的
  并行批次是高频路径；资源声明模型也能覆盖未来的 subagent/HTTP/MCP 工具。
  pi 的 per-file 队列（只管同文件写）与 opencode 的全串行是两个更弱的备选。
- [x] 是否采用 `resolveExecution` 两阶段？→ **采用**。直接动机是 Lcoder 自身的
  交互式权限确认：prepare（校验+权限，串行）/ run（执行，可并发）拆分后，
  并行 edit 不会同时弹多个确认框。两阶段也正好让 access 声明在参数校验
  之后、执行之前计算，路径已被 `ResolvePathAccess` 规范化。
