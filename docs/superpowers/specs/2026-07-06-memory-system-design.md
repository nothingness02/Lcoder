# Lcoder 持久记忆系统设计

> **范围**：为 Lcoder agent 引入跨会话持久记忆，参考 Hermes Agent 的 `MEMORY.md` / `USER.md` 设计，但适配 Lcoder 现有的 `contextmgr` 和工具体系。

**目标**：让 agent 能够记住用户偏好、项目约定、环境事实，并在新会话启动时自动注入系统提示词，同时保持实现简洁、可测试、可扩展。

**非目标**：
- 不引入外部向量数据库或语义搜索（session_search 等后续再议）。
- 不实现后台自动审查/技能写入（background review）。
- 不实现安全扫描（prompt-injection / credential leak 检测）作为 MVP。

---

## 1. 存储模型

### 1.1 文件位置

记忆文件分全局和项目两层：

| 文件 | 用途 | 默认字符上限 |
|---|---|---|
| `~/.lcoder/memory/MEMORY.md` | 全局 agent 笔记（跨项目的环境事实、经验教训） | 2,200 |
| `~/.lcoder/memory/USER.md` | 全局用户档案（偏好、沟通风格、身份） | 1,375 |
| `<repo>/.lcoder/memory/MEMORY.md` | 项目级 agent 笔记（技术栈、约定、已完成工作） | 2,200 |
| `<repo>/.lcoder/memory/USER.md` | 项目级用户偏好（可选，用于覆盖/补充全局） | 1,375 |

加载顺序：
1. 读取全局 `USER.md`。
2. 读取项目 `USER.md`（若存在，追加到全局之后）。
3. 读取全局 `MEMORY.md`。
4. 读取项目 `MEMORY.md`（若存在，追加到全局之后）。

最终合并为两个逻辑存储区：`user` 和 `memory`。

### 1.2 文件格式

参考 Hermes，采用单文件 + `§` 分隔符：

```text
══════════════════════════════════════════════
MEMORY [project] [67% — 1,474/2,200 chars]
══════════════════════════════════════════════
Project uses Go 1.25, contextmgr for budgeting, no ORM.
§
CI runs on GitHub Actions; always run go vet before push.
§
User prefers concise Chinese replies.
```

- 头部仅用于人工可读性，解析时忽略前两行或直到第一个非标题/非空行。
- 条目可为多行。
- `§` 作为条目分隔符，单独一行。

### 1.3 容量限制

- 每个存储区有字符上限。
- 写入操作若会导致超限，工具返回错误并附带当前条目列表，agent 需自行合并或删除。
- 不自动压缩或静默截断。

---

## 2. 上下文注入

### 2.1 新增 BlockKind

在 `pkg/contextmgr/block.go` 新增：

```go
BlockMemory      BlockKind = "memory"       // agent 个人笔记
BlockUserProfile BlockKind = "user_profile" // 用户档案
```

两者均为 `StabilityStable`，视为 system block（`IsSystemBlock` 返回 true），合并进 `SystemPrompt`。

### 2.2 注入顺序与优先级

`DefaultBlockOrder` 扩展为：

```go
system -> mode -> skills -> project_docs -> memory -> user_profile -> summary -> retrieval -> recent
```

优先级：

| Block | Priority |
|---|---|
| system | 100 |
| mode | 100 |
| skills | 90 |
| project_docs | 80 |
| memory | 75 |
| user_profile | 70 |

优先级低于核心 system/project/skills，确保记忆膨胀时 contextmgr 的 `StaticRatio` 会优先丢弃记忆块，而不是核心指令。

### 2.3 缓存

memory 块标记 `CacheHintBreakpoint`，与 skills/project_docs 一起构成可缓存前缀。

---

## 3. `memory` 工具

### 3.1 Schema

新增内置工具 `memory`，注册到 `pkg/tools/builtin/memory.go`：

```yaml
name: memory
description: |
  Manage persistent memory across sessions.
  Use this to save user preferences, project conventions, environment facts, or lessons learned.
parameters:
  type: object
  required: [action, target]
  properties:
    action:
      type: string
      enum: [add, replace, remove]
    target:
      type: string
      enum: [memory, user]
    content:
      type: string
      description: New entry text for add/replace.
    old_text:
      type: string
      description: Short unique substring of the entry to replace or remove.
```

### 3.2 操作语义

- **add**：追加条目。若内容已存在则返回成功但不重复写入。
- **replace**：用 `old_text` 子串匹配唯一条目并替换。匹配多个或零个返回错误。
- **remove**：用 `old_text` 子串匹配唯一条目并删除。

### 3.3 容量管理

- 每次写操作前计算写入后的总字符数。
- 超过上限返回错误：

```json
{
  "success": false,
  "error": "Memory at 2,100/2,200 chars. Adding this entry (250 chars) would exceed the limit. Consolidate now: use 'replace' to merge overlapping entries into shorter ones or 'remove' stale entries, then retry this add.",
  "current_entries": ["...", "..."],
  "usage": "2,100/2,200"
}
```

- agent 在同一轮内收到错误后可自行合并/删除再重试。

### 3.4 持久化

- 写操作成功后立即写回磁盘。
- 不等待会话结束，不依赖 checkpoint。
- 工具返回当前容量百分比和是否成功。

---

## 4. 组件划分

| 文件 | 职责 |
|---|---|
| `pkg/memory/store.go` | 发现全局/项目记忆目录、读写 `MEMORY.md` / `USER.md`、容量检查 |
| `pkg/memory/entry.go` | 解析/序列化条目、`§` 分隔、子串匹配、重复检测 |
| `pkg/memory/limits.go` | 默认字符上限常量 |
| `pkg/tools/builtin/memory.go` | `memory` 内置工具实现 |
| `pkg/contextmgr/block.go` | 新增 `BlockMemory`、`BlockUserProfile` |
| `pkg/agentsetup/setup.go` | 构造 `memory.Store`，加载并注入 context blocks |

---

## 5. 数据流

1. **启动/恢复会话**：
   - `agentsetup.NewContextManager` 创建 `memory.Store`。
   - `Store.Load()` 读取全局+项目 `USER.md` / `MEMORY.md`。
   - 若内容非空，创建 `BlockUserProfile` / `BlockMemory` 并加入 `contextmgr`。

2. **运行中写入**：
   - agent 调用 `memory` 工具。
   - `memory.Tool` 调用 `Store.Add/Replace/Remove`。
   - store 解析当前条目、执行修改、检查字符上限、写回文件。

3. **下一轮/新会话**：
   - `contextmgr` 重新从 store 加载最新文件内容，更新后的记忆进入 `SystemPrompt`。

---

## 6. 错误处理

| 场景 | 行为 |
|---|---|
| 子串匹配失败 | 返回错误：`old_text "..." did not match any entry` |
| 子串匹配多个 | 返回错误：`old_text "..." matched N entries; provide a more specific substring` |
| 字符超限 | 返回错误、当前条目列表、usage |
| 文件 IO 失败 | 返回错误，不修改内存状态 |
| 目录不存在 | store 自动创建目录；空文件视为空记忆 |

---

## 7. 配置（MVP 可选）

初始版本可先用常量，后续在 `~/.lcoder/config.yaml` 中暴露：

```yaml
memory:
  enabled: true
  memory_char_limit: 2200
  user_char_limit: 1375
```

MVP 阶段至少支持 `enabled` 开关；若 `enabled: false`，`agentsetup` 不加载记忆块，工具也不注册。

---

## 8. 测试策略

- **单元测试**：
  - `pkg/memory`：解析、子串匹配、重复检测、容量超限。
  - `pkg/tools/builtin/memory_test.go`：工具参数校验、写盘后读取、错误路径。

- **集成测试**：
  - 构造临时 home + repo 目录，agent 调用 `memory(add)` 后，验证 `contextmgr.SystemPrompt()` 包含新条目。
  - 验证项目级记忆追加在全局记忆之后。

- **不引入外部依赖**：测试仅使用临时目录和文件系统。

---

## 9. 后续扩展点

- **session_search**：基于 SQLite/FTS 搜索历史会话（非 MVP）。
- **后台审查（background review）**：会话结束后由辅助模型自动提炼记忆（非 MVP）。
- **写入审批（write_approval）**：默认自由写入，后续可配置为 pending/approve/reject 模式。
- **安全扫描**：对新增记忆做 prompt-injection / credential leak 检测。
- **外部记忆提供商**：保持接口可插拔，未来可接入向量数据库。

---

## 10. 与 Hermes 设计的对应关系

| Hermes | Lcoder MVP |
|---|---|
| `~/.hermes/memories/MEMORY.md` | `~/.lcoder/memory/MEMORY.md` + `<repo>/.lcoder/memory/MEMORY.md` |
| `~/.hermes/memories/USER.md` | `~/.lcoder/memory/USER.md` + `<repo>/.lcoder/memory/USER.md` |
| `memory` 工具 add/replace/remove | 同名内置工具 |
| 字符上限 2200/1375 | 沿用，通过 `pkg/memory/limits.go` 常量定义 |
| 系统提示词冻结快照 | 通过 `contextmgr.StabilityStable` + `CacheHintBreakpoint` 实现 |
| 后台审查 | MVP 不做 |
| session_search | MVP 不做 |

---

## 11. 风险与缓解

| 风险 | 缓解 |
|---|---|
| 记忆过多占用上下文 | 字符上限 + `StaticRatio` 双重保护 |
| agent 写入错误假设 | MVP 自由写入，后续可开启 `write_approval`；用户可手动编辑文件 |
| 项目级与全局记忆冲突 | 项目级追加在后，语义上为补充/覆盖 |
| 格式解析失败 | 容错解析：忽略标题行，按 `§` 分隔；IO 错误不破坏原文件 |

---

**下一步**：本设计通过评审后，使用 `superpowers:writing-plans` 编写详细实现计划。
