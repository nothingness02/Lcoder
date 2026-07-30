# 工具执行并发优化

> 状态: 待实施 | 优先级: 低 | 参考: Kimi Code `ToolAccesses` + `ToolScheduler`

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

## 参考实现：Kimi Code

Kimi Code 不使用静态标签。每个工具通过 `resolveExecution()` 声明访问的资源路径：

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

调度器根据资源冲突动态决定并发 vs 串行：

```typescript
// ToolScheduler
schedule(task) {
    if (conflictsWithAny(task, active) || conflictsWithAny(task, queued)) {
        queued.push(task);  // 冲突 → 等待前面的完成
    } else {
        start(task);        // 无冲突 → 并发
    }
}
```

冲突判定规则：

```
两个 access 冲突，当：
  1. 至少有一个是写操作（write / readwrite）
  2. 且路径重叠（同文件，或一个涉及目录递归另一个在该目录下）

read + read     → 不冲突（都是只读）
read + write    → 冲突（同一文件）
edit a.go + edit b.go → 不冲突（不同文件）
bash            → kind:'all' → 和所有工具冲突
```

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
    if a.Kind == OpRead && b.Kind == OpRead {
        return false  // 只读 + 只读 = 不冲突
    }
    if a.Kind == OpSearch && b.Kind == OpSearch {
        return false
    }
    // 有写操作 → 检查路径是否重叠
    return pathOverlaps(a, b)
}
```

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
```

## 影响评估

| 维度 | 影响 |
|------|------|
| 正确性 | 无影响。当前串行是正确子集 |
| 性能 | 多文件 edit 场景可提速（LLM 很少这样用） |
| 接口兼容 | 需新增 `AccessDeclarer` 可选接口 |
| 改动量 | ~150 行（新 access.go + scheduler + 工具适配） |
| 风险 | 低。可渐进迁移，未实现接口的工具保持串行 |

## 待决策

- [ ] 是否值得为低概率场景（同时编辑多文件）增加复杂度？
- [ ] 是否直接采用 Kimi Code 的 `resolveExecution` 二阶段模型（resolve + execute），而不是当前的单阶段 `Execute`？
