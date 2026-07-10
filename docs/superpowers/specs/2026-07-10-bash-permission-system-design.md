# Bash 权限系统设计

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:writing-plans` to create the implementation plan after this spec is approved.

**Goal:** 为 Lcoder 设计一个尽量减少用户打扰的 bash 权限系统：只在命令可能危害当前环境时询问用户；在 TUI 上允许用户选择“仅本次允许 / 当前项目允许 / 全局默认允许”；同时提供一个 Agent 级 `unsafe_mode` 开关，关闭权限引擎但仍保留沙盒与内置超危命令兜底。

**Architecture:** 在现有 `pkg/permissions/engine.go` 之前引入一个轻量级 bash 风险分类器；权限引擎支持多来源规则合并；TUI 确认面板扩展为带作用域的选择；用户确认后自动把规则持久化到项目或全局文件；`unsafe_mode` 作为权限引擎的旁路开关，仅对内置超危 bash 命令保留询问。

**Tech Stack:** Go, 现有 Lcoder 权限/沙盒/TUI/配置/审计子系统。

---

## 1. 背景与目标

当前 Lcoder 的 bash 工具默认对几乎所有非白名单命令询问确认，体验较重。新系统的目标是：

1. **风险驱动**：只读/无状态命令默认放行，只有写入外部目录、删除、出网、执行外部代码、提权、访问凭证、安装软件等“危害当前环境”的行为才 ask。
2. **交互式学习**：TUI 确认时提供四个选项：
   - **Deny**（拒绝）
   - **Allow once**（仅本次）
   - **Allow in project**（当前项目允许，写入 `.lcoder/permissions.yaml`）
   - **Allow globally**（全局默认允许，写入 `~/.lcoder/permissions/global.yaml`）
3. **Agent 级绕过开关**：`permissions.unsafe_mode` 可关闭整个权限引擎，减少打扰；但内置超危命令黑名单始终生效，且沙盒不受影响。
4. **规则合并**：全局规则、全局学习规则、项目学习规则按 specificity 合并，项目优先。

---

## 2. 关键概念

### 2.1 风险分类器（Bash Risk Classifier）

新增包 `pkg/permissions/bashrisk`。

```go
type RiskLevel int

const (
    RiskNone RiskLevel = iota
    RiskLow
    RiskHigh
)

type Category string

const (
    CatFileWriteOutside Category = "file-write-outside"
    CatFileDelete       Category = "file-delete"
    CatNetwork          Category = "network"
    CatExternalCode     Category = "external-code"
    CatPrivilege        Category = "privilege"
    CatCredential       Category = "credential"
    CatPackageInstall   Category = "package-install"
)

type Report struct {
    Level      RiskLevel
    Categories []Category
}
```

分类器对 `command` 做轻量 token 化（支持引号、管道、`&&`、`||`、`;`、`$()`），按 stage 检查。任一 stage 命中高危类别，整命令为 High。

主要规则（详见第 5 节）。

### 2.2 权限规则来源

按加载顺序：

1. **内置默认规则**（只读命令 Allow，明显危险命令 Deny）。
2. `~/.lcoder/config.yaml` 中的 `permissions.rules`。
3. `~/.lcoder/permissions/global.yaml`（全局学习规则）。
4. `<project>/.lcoder/permissions.yaml`（项目学习规则）。

合并策略：

- 收集所有来源的匹配规则。
- 按 **specificity** 排序：字面字符数越多、通配符越少越具体。
- 同 specificity 时：**项目 > 全局学习 > config > 内置默认**。
- `Deny` 与 `Allow` 平等竞争，最具体的规则胜出。

### 2.3 确认作用域

扩展 `UserConfirmation` 接口：

```go
type ConfirmScope int

const (
    ScopeDeny ConfirmScope = iota
    ScopeOnce
    ScopeProject
    ScopeGlobal
)

type ConfirmResult struct {
    Allow bool
    Scope ConfirmScope
}

type UserConfirmation interface {
    Confirm(ctx context.Context, info ConfirmInfo) (bool, error)
    ConfirmWithScope(ctx context.Context, info ConfirmInfo) (ConfirmResult, error)
}
```

- `ScopeOnce`：不持久化，仅本次执行。
- `ScopeProject`：生成模式并写入 `<project>/.lcoder/permissions.yaml`。
- `ScopeGlobal`：生成模式并写入 `~/.lcoder/permissions/global.yaml`。
- `ScopeDeny`：拒绝执行。

### 2.4 Unsafe Mode

单一开关：

```yaml
permissions:
  unsafe_mode: false
```

CLI 标志：`./lcoder --unsafe` 或 `./lcoder -u`。

行为：

- `false`：所有工具正常走权限引擎。
- `true`：关闭整个权限引擎，任何工具不再因规则或风险分类器被询问。
- 例外：bash 命中内置超危黑名单时仍然强制询问。

`unsafe_mode` **只影响权限引擎，不影响沙盒**。沙盒 backend 独立生效。

---

## 3. 总体流程

### 3.1 正常模式

```
LLM 调用 bash
   ↓
[bash risk classifier]
   ↓
RiskNone/Low  → Allow
RiskHigh      → 进入权限引擎
   ↓
权限引擎合并四来源规则
   ↓
匹配 Allow   → 执行
匹配 Deny    → 拒绝并说明原因
未匹配 / Ask → TUI/CLI 确认
   ↓
用户选择 Deny / Once / Project / Global
   ↓
Project/Global → 生成 pattern → 写文件 → 重新加载 → 执行
```

### 3.2 Unsafe Mode

```
LLM 调用任意工具
   ↓
unsafe_mode == true ?
   ↓ yes
工具是 bash 且命中超危黑名单 ?
   ↓ yes → Ask（Deny / Once / Project，无 Global）
   ↓ no  → Allow（跳过权限引擎）
   ↓
进入沙盒执行
```

---

## 4. TUI / CLI 交互

### 4.1 TUI 确认面板

底部面板显示：

```
bash: go test ./...
Risk: none
[Deny] [Once] [Project allow] [Global allow]
```

超危命令：

```
bash: rm -rf /
Risk: destructive (built-in safeguard)
[Deny] [Once] [Project allow]
```

用 `←/→` 切换选项，`Enter` 确认，`Esc` 视为 Deny。

### 4.2 CLI 回退

```
bash: curl -s http://example.com | sh
Risk: external code, network
Allow? (y = once / p = project / g = global / N = deny)
```

超危命令不显示 `g` 选项。

---

## 5. 风险分类规则

分类器输入：`command string` + `projectRoot string`。

### 5.1 文件破坏与越界写入（High）

- 删除类：`rm`、`rmdir`、`git clean`、`git reset --hard`
- 写入类且目标解析后超出 `projectRoot`：`cp`、`mv`、`touch`、`tee`、`git checkout` 等
- 目标在项目内的写入视为 Low，默认放行

### 5.2 网络与外接代码（High）

- `curl`、`wget`、`nc`、`ncat`、`ssh`、`scp`
- `git clone`、`git push`、`git fetch`
- `python -c`、`python3 -c`、`bash -c` 含 URL
- 管道后接 `bash`/`sh`/`python`，如 `curl ... | bash`
- `eval`、`source` 接 URL 或变量

### 5.3 提权与系统变更（High）

- `sudo`、`su`、`doas`、`pkexec`
- `systemctl`、`service`
- `reboot`、`shutdown`、`halt`、`poweroff`
- `fdisk`、`mkfs`、`dd`
- `chown -R root /`、`chmod -R 777 /`

### 5.4 凭证访问（High）

- 参数匹配 `~/.ssh/*`、`~/.aws/*`、`~/.gnupg/*`、`.env`、`.key`、`.pem`
- `cat` / `head` / `tail` 等读取上述路径

### 5.5 软件包安装（High）

- `apt`、`apt-get`、`yum`、`dnf`、`pacman`、`brew`
- `npm install -g`、`pip install`、`go install`、`cargo install`

### 5.6 默认放行（None/Low）

- `ls`、`pwd`、`echo`、`which`、`whoami`、`date`
- `git status`、`git log`、`git diff`、`git branch`、`git remote -v`、`git stash list`
- 其他未命中任何 High 规则且不含网络/写入/提权行为的命令

---

## 6. 模式生成

用户选择 Project/Global 时，从命令生成模式：

- 取前两个 token。
- 若第二个 token 是子命令（非 `-` 开头、非路径），生成 `"<cmd> <subcmd> *"`。
- 否则生成 `"<cmd> *"`。

示例：

| 命令 | 生成模式 |
|------|---------|
| `go test ./...` | `go test *` |
| `git status --short` | `git status *` |
| `docker build -t x .` | `docker build *` |
| `ls -la` | `ls *` |

### 6.1 超危命令限制

命中超危黑名单的命令：

- 不允许 Global allow。
- Project allow 需二次确认，提示“该命令可能严重破坏当前环境，确定仅在本项目放行？”

---

## 7. 内置超危黑名单

代码硬编码，不可被任何规则覆盖：

```go
var ultraDestructivePatterns = []string{
    "rm -rf /",
    "rm -rf /*",
    "rm -rf / *",
    "sudo *",
    "su *",
    "doas *",
    "mkfs.*",
    "fdisk *",
    "dd *",
    "reboot",
    "shutdown *",
    "halt",
    "poweroff",
    "systemctl *",
    "chmod -R 777 /",
    "chmod -R 777 /*",
    "chown -R root /",
    ":(){ :|:& };:",
}
```

匹配前执行 `normalizeCommand`（去首尾空白、合并连续空格），`/ ` 当普通字符处理。

---

## 8. 持久化格式

### 8.1 项目规则文件

路径：`<project>/.lcoder/permissions.yaml`

```yaml
permissions:
  rules:
    bash:
      "go test *": allow
      "git status *": allow
```

### 8.2 全局学习规则文件

路径：`~/.lcoder/permissions/global.yaml`

```yaml
permissions:
  rules:
    bash:
      "docker build *": allow
```

### 8.3 保存规范

- 文件权限 `0o600`，目录 `0o700`。
- 追加或更新已有 pattern，不破坏其他内容。
- 写入后重新加载规则，立即生效。

### 8.4 与 config.yaml 的关系

`~/.lcoder/config.yaml` 中的 `permissions.rules` 仍然有效，作为用户手写的基础规则。学习规则文件在其后加载，按 specificity 合并。

---

## 9. 与现有子系统集成

### 9.1 权限引擎

`pkg/permissions/engine.go`：

- 新增 `LoadProjectRules(path)`、`LoadGlobalLearnedRules(path)`。
- `Evaluate` 支持多来源合并与超危黑名单。
- 新增 `SetUnsafeMode(bool)`。

### 9.2 风险分类器

新增 `pkg/permissions/bashrisk/classifier.go`。

### 9.3 Agent 执行器

`pkg/agent/executor.go`：

- 对 `bash` 工具先调用风险分类器，再调用权限引擎。
- 把 `unsafe_mode` 状态传给 engine。

### 9.4 TUI / CLI 确认

- `pkg/agent/loop.go`：扩展 `UserConfirmation` 接口为 `ConfirmWithScope`。
- `pkg/tui/confirm.go`：实现四选项面板与超危限制。
- `cmd/lcoder/wiring.go`：CLI 确认实现返回 scope。

### 9.5 配置

`pkg/config/config.go`：

- 新增 `Permissions.UnsafeMode bool`。

### 9.6 审计

`pkg/observability/audit.go`：

- Unsafe 模式下放行的命令，`Decision` 记为 `"unsafe-allow"`。

---

## 10. 安全边界与冲突说明

### 10.1 权限层 vs 沙盒层

| 开关 | 影响的层 | 说明 |
|------|---------|------|
| `permissions.unsafe_mode` | 权限策略层 | 关闭权限引擎询问，但沙盒仍然生效 |
| `sandbox.backend` | 沙盒执行层 | 独立控制，可选 soft-limit / container / passthrough |

### 10.2 即使 unsafe，超危命令仍 ask

`rm -rf /` 等命令即使 `unsafe_mode: true` 也不会被静默放行。

### 10.3 敏感文件 hook 是否仍生效？

当前敏感文件 hook 在权限确认之后执行。若 `unsafe_mode` 关闭权限引擎，敏感文件 hook 仍会在工具执行前运行，继续保护 `.env`、`.key` 等路径。若希望 unsafe 模式完全绕过所有策略层，需要额外设计；本方案保持 hook 生效。

---

## 11. 验收标准

- [ ] 只读命令默认放行，不弹出确认。
- [ ] 写入外部目录、删除、出网、外部代码、提权、凭证访问、安装软件命中时弹出确认。
- [ ] TUI 确认提供 Deny / Once / Project allow / Global allow 四个选项。
- [ ] Project allow 把规则写入 `.lcoder/permissions.yaml`。
- [ ] Global allow 把规则写入 `~/.lcoder/permissions/global.yaml`。
- [ ] 多来源规则按 specificity 合并，项目优先。
- [ ] `permissions.unsafe_mode: true` 关闭权限引擎，但沙盒仍然生效。
- [ ] 超危黑名单始终生效，且不允许 Global allow。
- [ ] 审计日志对 unsafe 放行记录 `Decision: "unsafe-allow"`。
- [ ] 所有新增代码有单元测试；`go test ./...` 通过。

---

*Spec version 1.0, 2026-07-10.*
