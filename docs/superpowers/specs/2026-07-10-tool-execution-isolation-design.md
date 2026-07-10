# Lcoder 工具执行隔离与去重设计

> **范围**：解决工具执行层三个问题：重复调用污染上下文、失败后错误传播、以及失败调用对本地环境的非预期变更。本设计覆盖 `pkg/agent/executor.go` 与 `pkg/tools/builtin` 中 `read/ls/grep/find`、`write/edit`、`bash` 三类工具。

**目标**：
- 避免同一次 turn 内对只读幂等工具的重复执行。
- 对文件写操作引入两阶段执行，失败时本地文件不回退也可保持原状。
- 对 `bash` 命令在沙箱中执行，失败时丢弃沙箱层，仅把 stdout/stderr/exit code 以及显式产物回传给 LLM。

**非目标**：
- 不实现跨 turn 的长期工具结果缓存（session 级别缓存后续再议）。
- 不实现通用的事务/回滚框架（只覆盖 `write/edit` 的备份回滚）。
- 不自动把沙箱内全部文件系统变更合并回本地；只允许显式声明的输出文件。

---

## 1. 同 turn 工具去重

### 1.1 去重键

在同一 turn 内，按归一化后的 `(tool_name, normalized_args)` 做缓存。

- `normalized_args` 需稳定：`map` 按键名排序后序列化为 JSON；数值统一用 `float64`/`int` 的最简形式；去掉空白差异。
- `tool_call_id` 不参与键：LLM 可能因重试生成不同 `id`，但语义相同。
- 仅当工具声明 `ExecutionMode == models.ExecutionSequential` 时才缓存；并行模式下各调用副作用可能相互依赖，不缓存。

### 1.2 可缓存工具白名单

默认仅对以下只读、幂等工具启用去重：

| 工具 | 说明 |
|---|---|
| `read` | 读取同一文件路径，同一 turn 内结果不变 |
| `ls` | 列出同一目录，同一 turn 内结果不变 |
| `grep` | 在固定路径/模式上搜索 |
| `find` | 在固定路径/模式上搜索 |
| `fspath` | 路径检查 |

写工具（`write/edit`）和 `bash` **不参与去重**，因为它们的副作用是设计目的，必须让 LLM 看到每次调用的独立结果。

### 1.3 命中后的行为

- 返回上一份结果的 **深拷贝**，避免后续 hook 修改污染缓存。
- 在结果 `Details` 中附加 `"deduplicated": true`，方便 observability 统计。
- 仍然生成一条新的 `ToolResultContent`，`ToolCallID` 与当前调用对齐，确保 LLM 的消息对完整。

---

## 2. `write` / `edit` 两阶段执行

### 2.1 阶段划分

```
Stage 1: dry-run
  - 校验参数、路径边界、权限。
  - 计算最终文件内容或 diff。
  - 不写入目标文件；可写入临时文件做内容校验。

Stage 2: commit
  - 备份原文件到 `<path>.lcoder.bak`。
  - 原子写入目标文件（temp + rename）。
  - commit 失败时，用备份恢复。
```

### 2.2 `edit` 工具

当前 `edit.go` 是顺序应用 edits。改进后：

1. 先把原文件读到内存。
2. 在内存副本上顺序应用所有 edits；任何一条不匹配即返回错误，**此时磁盘未动**。
3. 全部匹配后，把内存副本写入临时文件，重命名为目标文件；失败则用备份恢复。

### 2.3 `write` 工具

1. 在临时文件写入完整内容。
2. 对临时文件做必要校验（如 JSON/YAML 解析，若配置要求）。
3. 备份原文件后 rename 临时文件到目标路径。

### 2.4 回滚语义

| 失败点 | 本地状态 |
|---|---|
| dry-run 参数/路径/权限校验失败 | 完全无变更 |
| edits 不匹配 | 完全无变更 |
| 备份或写入 IO 失败 | 尝试恢复备份；若恢复也失败，返回错误并保留备份路径 |
| rename 成功后读取校验失败 | 视为已提交成功；校验失败作为 tool_result 错误返回，但不自动撤销 |

---

## 3. `bash` 沙箱执行 + 显式产物回传

### 3.1 执行模型

所有 `bash` 调用默认进入沙箱（`sandbox.Sandbox.Exec`）。沙箱已有限制：

- 可读/写目录受 `FilesystemConfig` 控制。
- 网络受 `NetworkConfig` 控制。
- 超时、进程数、CPU/内存限制。

### 3.2 不整体回写沙箱文件系统

沙箱内的文件变更**不会**自动同步到本地工作目录。原因：

- 无法可靠判断哪些是意图中的产物（如编译生成的 `.pyc`、日志、缓存）。
- 自动合并可能覆盖本地未提交修改。
- 网络、数据库等副作用无法通过丢弃沙箱层撤销。

### 3.3 显式产物机制

如果 `bash` 需要把文件带回本地，调用方在参数中声明输出文件列表：

```yaml
command: "python generate_report.py --out /tmp/report.md"
outputs:
  - "/tmp/report.md"
```

执行流程：

1. 沙箱运行命令。
2. 若退出码为 0，把 `outputs` 中列出的文件从沙箱复制到本地对应相对路径（或 `cwd`）。
3. 若退出码非 0 或超时，直接丢弃沙箱层，`outputs` 不复制。
4. stdout/stderr/exit code 始终返回给 LLM，无论成功失败。

### 3.4 无产物命令

大多数 `bash` 命令（`git status`、`pytest`、`grep` 等）只产生 stdout/stderr。它们运行在沙箱中，失败后只丢弃沙箱层即可，本地不受影响。

---

## 4. 错误传播（保持不变，但语义更清晰）

- `registry.Execute` 返回 `(models.ToolExecutionResult, isError)`。
- `write/edit` dry-run 失败 => `isError=true`，本地无变更。
- `write/edit` commit 失败并恢复 => `isError=true`，本地无变更；若恢复失败，结果中附加备份路径。
- `bash` 非零退出或超时 => `isError=true`，沙箱层已丢弃；stdout/stderr 仍返回。
- 去重命中 => `isError` 与原始结果一致。

---

## 5. 组件划分

| 文件 | 职责 |
|---|---|
| `pkg/agent/executor.go` | 同 turn 去重缓存、调度两阶段工具、调用沙箱执行 bash |
| `pkg/tools/builtin/edit.go` | 实现 dry-run + commit + 备份回滚 |
| `pkg/tools/builtin/write.go` | 实现 dry-run + commit + 备份回滚 |
| `pkg/tools/builtin/bash.go` | 解析 `outputs`，调用沙箱，成功后复制产物 |
| `pkg/tools/args.go` | 提供稳定的参数归一化辅助函数（用于去重键） |
| `pkg/sandbox/exec_*.go` | 提供隔离执行环境（已有） |

---

## 6. 数据流

### 6.1 去重路径

```
LLM ToolCallContent
  -> executor.executeOneToolCall
    -> 计算 (name, normalized_args)
    -> 命中缓存?
       yes -> 深拷贝结果，更新 ToolCallID，返回
       no  -> 继续执行，结果写入缓存
```

### 6.2 write/edit 路径

```
ToolCall
  -> ValidateArgs
  -> 权限确认
  -> BeforeToolCall hooks
  -> dry-run（计算最终内容 / 校验 edits）
     失败 -> 返回 IsError=true，磁盘未动
  -> commit（备份 -> temp -> rename）
     失败 -> 用备份恢复 -> 返回 IsError=true
  -> 返回成功结果
```

### 6.3 bash 路径

```
ToolCall
  -> ValidateArgs
  -> 权限确认
  -> sandbox.Exec(command, outputs)
     失败/非零退出 -> 丢弃沙箱层 -> 返回 stdout/stderr/exit_code，IsError=true
     成功 -> 复制 outputs 到本地 -> 返回 stdout/stderr/exit_code，IsError=false
```

---

## 7. 配置（MVP 可选）

初始版本可用常量 + 工具级白名单，后续在 `config.yaml` 暴露：

```yaml
execution:
  deduplicate_read_tools: true
  edit_backup_suffix: ".lcoder.bak"
  bash_default_sandbox: true
  bash_output_base_dir: ".lcoder/sandbox-outputs"
```

MVP 至少支持：

- 去重开关：默认开启。
- 备份后缀：固定为 `.lcoder.bak`。
- bash 必须走沙箱：若沙箱后端为空，则回退到 soft-limit（已有行为），不允许无沙箱执行。

---

## 8. 测试策略

### 8.1 单元测试

- `pkg/agent/executor_test.go`：
  - 同 turn 两次相同 `read` 调用只执行一次。
  - 同名不同参调用不命中缓存。
  - 写工具不参与去重。

- `pkg/tools/builtin/edit_test.go`：
  - edits 不匹配时原文件不变。
  - commit 中断后成功恢复备份。

- `pkg/tools/builtin/write_test.go`：
  - 写入过程中进程被杀，原文件保留。
  - 临时文件被清理。

- `pkg/tools/builtin/bash_test.go`：
  - 非零退出时沙箱层丢弃，outputs 不复制。
  - 成功时 outputs 正确复制到本地。

### 8.2 集成测试

- 使用 `FakeSandbox` 验证 bash 路径沙箱调用参数与产物复制。
- 构造真实临时目录验证 edit/write 的备份恢复。

### 8.3 不引入外部依赖

- 测试只使用临时目录、`FakeSandbox`、`FakePermissionEngine`。

---

## 9. 风险与缓解

| 风险 | 缓解 |
|---|---|
| 去重导致并行调用结果不一致 | 仅对串行模式工具启用；并行模式不缓存 |
| 写工具 commit 失败后恢复也失败 | 保留 `.lcoder.bak`，结果中附带路径，提示用户手动恢复 |
| bash outputs 路径声明遗漏 | LLM 可通过 stdout 看到产物路径，未声明的产物不自动回传 |
| 沙箱不可用导致 bash 无法执行 | MVP 要求至少 soft-limit；无沙箱时拒绝执行 |
| 备份文件污染工作区 | 备份后缀固定，可在 `.gitignore` 模板中默认忽略 |

---

**下一步**：本设计通过评审后，使用 `superpowers:writing-plans` 编写详细实现计划。
