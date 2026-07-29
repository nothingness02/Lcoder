# Lcoder 用户手册

> 本文档是 Lcoder 的详细用户手册，面向第一次接触 Lcoder 的终端用户。如果你是开发者，想扩展 Lcoder，请阅读 `DEVELOPER_GUIDE.md`。

## 目录

1. [简介](#1-简介)
2. [安装与环境要求](#2-安装与环境要求)
3. [首次配置](#3-首次配置)
4. [快速开始](#4-快速开始)
5. [CLI 命令参考](#5-cli-命令参考)
6. [TUI 深度使用](#6-tui-深度使用)
7. [Agent 模式（Modes）](#7-agent-模式modes)
8. [技能（Skills）](#8-技能skills)
9. [会话与分支](#9-会话与分支)
10. [安全与权限](#10-安全与权限)
11. [扩展工具](#11-扩展工具)
12. [代码索引](#12-代码索引)
13. [可观测性](#13-可观测性)
14. [故障排查](#14-故障排查)
15. [配置字段完整参考](#15-配置字段完整参考)
16. [附录：环境变量与文件路径速查](#16-附录环境变量与文件路径速查)

---

## 1. 简介

Lcoder 是一个用 Go 编写的**极简、可扩展的 SWE（Software Engineering）agent 运行时框架**。它把大语言模型（LLM）、工具调用、会话管理、权限控制和可观测性整合在一个命令行程序中，既可以作为日常编码助手使用，也可以作为更复杂 agent 系统的基础平台。

### 1.1 核心特性

- **进程内 LLM 引擎**：不依赖外部 SDK，直接通过手写 HTTP+SSE 适配器与 OpenAI 兼容端点、Anthropic 端点通信。
- **多模式 agent**：内置 `code`、`plan`、`explore`、`review`、`test` 等模式，针对编码、设计、探索、评审、测试等场景优化 system prompt。
- **丰富的工具生态**：
  - 内置工具：文件读写、编辑、`bash`、代码索引搜索、记忆、子代理等。
  - HTTP 工具：向任意 POST 端点发送请求。
  - MCP 服务器：通过 stdio、SSE、Streamable HTTP 接入 Model Context Protocol 服务器。
- **终端交互界面（TUI）**：基于 `charmbracelet/bubbletea`，支持消息历史、工具结果折叠/展开、会话选择、扩展面板等。
- **会话与分支**：会话以 JSONL 形式保存，每条消息记录 `parent_id`，支持从任意消息分叉。
- **安全默认**：具有破坏性的工具默认需要确认，支持 `allow/ask/deny` 三级权限规则。
- **可观测性**：自动记录 LLM 调用、token 消耗、工具执行、耗时等指标，支持导出 HTML / SQLite / Prometheus。
- **代码索引**：可选的 SQLite 持久化代码图索引，支持 Go / TypeScript / JavaScript / Python，可自动注入相关上下文。

### 1.2 适合什么场景

- 日常编码：解释、重构、补全、调试代码。
- 代码评审：让 agent 针对指定文件给出评审意见。
- 方案设计：在 `plan` 模式下进行高层架构设计。
- 自动化工作流：通过 HTTP 工具或 MCP 服务器把 Lcoder 接入 CI/CD、部署、文档生成等流程。
- 自定义 agent：基于 Lcoder 的扩展机制编写自己的工具、模式、hook、exporter。

### 1.3 与类似工具的差异

| 维度 | Lcoder | Claude Code / Cursor 等商业工具 |
|---|---|---|
| 部署方式 | 本地源码构建，完全自托管 | 通常是闭源客户端或 IDE 插件 |
| 扩展机制 | 进程外扩展、HTTP 工具、MCP 服务器 | 多由官方提供，扩展受限 |
| 模型路由 | 进程内引擎，支持任意 OpenAI 兼容端点 | 通常只支持官方模型 |
| 会话存储 | JSONL，可手动检查、分叉、备份 | 通常私有存储 |
| 权限控制 | 细粒度 glob 规则，本地审计日志 | 由产品决定 |

---

## 2. 安装与环境要求

### 2.1 前提条件

- **Go**：项目使用 Go 1.25.4 或更高版本（以 `go.mod` 中声明为准）。
- **操作系统**：Linux、macOS、Windows 均可编译运行。TUI 依赖终端支持 ANSI 转义序列。
- **API key**：至少准备一个 LLM provider 的 API key（OpenAI、Anthropic、DeepSeek、Moonshot、DashScope 等）。

### 2.2 从源码构建

```bash
# 克隆仓库
git clone https://github.com/lcoder/lcoder.git
cd lcoder

# 构建二进制到当前目录
go build -o lcoder ./cmd/lcoder

# 验证
./lcoder --help
```

在 Windows 上，上述命令会生成 `lcoder.exe`。

### 2.3 安装到 PATH（可选）

```bash
# Linux / macOS
mkdir -p ~/.local/bin
cp lcoder ~/.local/bin/
# 确保 ~/.local/bin 在 PATH 中

# Windows (PowerShell)
# 将 lcoder.exe 复制到已加入 PATH 的目录，例如 C:\Tools
```

### 2.4 升级

Lcoder 目前未提供自动更新命令。升级时请重新拉取源码并执行 `go build`。

---

## 3. 首次配置

Lcoder 启动时会按以下顺序加载配置：

1. 内置默认值（`config.DefaultConfig()`）。
2. `~/.lcoder/config.yaml`（或 `--config` 指定的文件）。
3. 环境变量覆盖（`LCODER_` 前缀，具体见各字段说明）。
4. 命令行 flag：`--model`、`--provider`、`--unsafe` 等。

### 3.1 复制示例配置

```bash
mkdir -p ~/.lcoder
cp configs/lcoder.yaml ~/.lcoder/config.yaml
```

### 3.2 选择模型与 provider

编辑 `~/.lcoder/config.yaml`：

```yaml
provider: openai
model: gpt-4o-mini
```

`provider` 可以是 `openai`、`anthropic`、`deepseek`、`moonshot`、`dashscope` 等。如果省略 `provider`，Lcoder 会尝试根据 `model` id 自动解析 provider。

### 3.3 设置 API key

**方式一：环境变量（推荐，最简单）**

```bash
export OPENAI_API_KEY=sk-...
export ANTHROPIC_API_KEY=sk-...
export DEEPSEEK_API_KEY=sk-...
```

Windows PowerShell：

```powershell
$env:OPENAI_API_KEY = "sk-..."
```

**方式二：`~/.lcoder/credentials.yaml`**

该文件由 TUI 配置向导自动写入，权限为 `0600`。

```yaml
openai:
  api_key: sk-...
  # base_url: https://api.openai.com/v1   # 可选
```

**方式三：在 `config.yaml` 中声明 provider 并内嵌 key**

```yaml
providers:
  moonshot:
    base_url: "https://api.moonshot.cn/v1"
    api_key: "{env:MOONSHOT_API_KEY}"
```

其中 `{env:VAR}` 会在启动时从环境变量读取。

**优先级**：`config.providers.<name>.api_key`（含 `{env:VAR}`） > `~/.lcoder/credentials.yaml` > 标准环境变量（如 `OPENAI_API_KEY`）。

### 3.4 TUI 首次启动向导

如果当前 provider 没有配置 API key，启动 TUI 时会自动弹出向导：

1. 选择 provider。
2. 选择 model。
3. 输入 API key。

完成后，key 会写入 `~/.lcoder/credentials.yaml`。

### 3.5 验证配置

```bash
./lcoder models          # 列出可用模型
./lcoder -p "你好"       # 运行一次单次对话
```

如果模型能正常返回，说明配置成功。

---

## 4. 快速开始

### 4.1 运行单次对话

单次对话执行一次用户提示后退出，适合脚本化使用。

```bash
./lcoder -p "列出当前目录的文件"

# 或使用位置参数
./lcoder "列出当前目录的文件"
```

输出会显示 assistant 的最终回复。工具执行过程默认不会逐条打印，但会在 TUI 或 JSON 模式下可见。

### 4.2 启动交互式 TUI

不传入任何参数即可启动 TUI：

```bash
./lcoder
# 或显式
./lcoder tui
```

在 TUI 中：

- 输入提示，按 `Enter` 发送。
- 按 `Shift+Enter` 在输入框内换行。
- 按 `Ctrl+C` 或 `Esc` 退出。

### 4.3 继续会话

Lcoder 会自动保存会话。退出后可以通过以下方式恢复：

```bash
./lcoder -c                              # 继续最近一次会话
./lcoder --session <id> -p "继续"        # 恢复指定会话并发送新消息
./lcoder tui --session <id>              # 在 TUI 中恢复指定会话
```

### 4.4 使用模式

模式会影响 system prompt，从而改变 agent 的行为风格：

```bash
./lcoder --mode plan -p "设计一个用户认证模块"
./lcoder --mode review -p "Review pkg/agent/loop.go"
./lcoder --mode test -p "为 pkg/llm/client.go 编写单元测试"
```

可用模式可通过 `./lcoder modes` 查看。

### 4.5 一个完整示例

```bash
# 1. 配置并验证
export OPENAI_API_KEY=sk-...
./lcoder models | grep gpt-4o

# 2. 在 plan 模式下让 agent 给出方案
./lcoder --mode plan -p "如何给这个项目添加一个缓存层？"

# 3. 进入 TUI 继续细化
./lcoder -c

# 4. 让 agent 实现方案
# 在 TUI 中输入："请根据上面的方案，在 pkg/cache 下实现一个 LRU 缓存"
```

---

## 5. CLI 命令参考

### 5.1 全局 Flags

以下 flag 属于 `lcoder` 主命令，**不能**用于 `tui` 等子命令。例如 `./lcoder tui --session <id>` 不会生效；要恢复会话请在主命令上指定：

```bash
./lcoder --session <id>       # 在 TUI 中恢复指定会话
./lcoder -c                   # 在 TUI 中继续最近一次会话
```

| Flag | 说明 |
|---|---|
| `--config PATH` | 指定配置文件路径，默认 `~/.lcoder/config.yaml`。 |
| `--model ID` | 临时覆盖配置文件中的模型。 |
| `--provider NAME` | 临时覆盖配置文件中的 provider。 |
| `--session ID` | 加载指定会话。 |
| `-c, --continue` | 继续最近一次会话。 |
| `--mode NAME` | 指定 agent 模式，默认 `code`。 |
| `-p, --prompt TEXT` | 传入单次提示，执行后退出。 |
| `--json` | 以 JSONL 事件流输出，而非 TUI/文本。 |
| `--unsafe` | 绕过权限引擎；极端危险命令仍需要确认。 |

> 只有 `--unsafe` 是 persistent flag，可以在子命令（如 `tui`）上使用。

### 5.2 子命令一览

| 命令 | 用法 | 说明 |
|---|---|---|
| `models` | `./lcoder models` | 列出模型目录中的可用模型。 |
| `skills` | `./lcoder skills` | 列出当前目录和 `~/.lcoder/skills` 下发现的技能。 |
| `sessions` | `./lcoder sessions` | 列出当前工作区的会话。 |
| `modes` | `./lcoder modes` | 列出可用 agent 模式。 |
| `stats` | `./lcoder stats <session-id>` | 显示指定会话的统计信息。 |
| `trace` | `./lcoder trace <session-id>` | 打印会话的可读 trace。 |
| `export` | `./lcoder export <session-id>` | 导出会话可观测数据，默认 HTML。 |
| `metrics` | `./lcoder metrics [port]` | 启动 Prometheus 指标端点，默认 `:9090`。 |
| `tui` | `./lcoder tui` | 启动交互式 TUI。要恢复会话请用 `./lcoder --session ID` 或 `./lcoder -c`。 |
| `install` | `./lcoder install SOURCE` | 安装扩展或包。 |
| `uninstall` | `./lcoder uninstall NAME` | 卸载扩展或包。 |
| `list-extensions` | `./lcoder list-extensions` | 列出已安装的扩展和包。 |
| `update` | `./lcoder update NAME` | 更新已安装的扩展或包。 |

### 5.3 `export` 命令详解

```bash
# 默认导出为 HTML
./lcoder export <session-id>

# 指定格式
./lcoder export <session-id> --format sqlite -o report.db
./lcoder export <session-id> --format markdown -o report.md
./lcoder export <session-id> --format prometheus -o metrics.txt
```

### 5.4 `install` 命令详解

```bash
# 从本地目录安装扩展
./lcoder install ./my-extension --name my-ext --local

# 从 git 仓库安装
./lcoder install https://github.com/acme/lcoder-ext-tools.git --name acme-tools
```

### 5.5 `sessions` 与会话恢复

```bash
./lcoder sessions
# 输出示例：
# abc123  2026-07-10 14:32
# def456  2026-07-11 09:15

./lcoder --session abc123 -p "继续昨天的工作"
```

## 6. TUI 深度使用

TUI（Terminal User Interface）是 Lcoder 的默认交互方式，基于 `charmbracelet/bubbletea` 构建。

### 6.1 启动与退出

```bash
./lcoder              # 启动新会话（默认进入 TUI）
./lcoder tui          # 显式启动 TUI
./lcoder -c           # 在 TUI 中继续最近一次会话
./lcoder --session <id>   # 在 TUI 中恢复指定会话
```

退出：

- `Ctrl+C`：发送 SIGINT，Lcoder 会尝试写入 crash checkpoint 后退出。
- `Esc`：关闭面板、取消当前操作或退出。

### 6.2 快捷键

| 快捷键 | 作用 |
|---|---|
| `Enter` | 发送当前输入的消息。 |
| `Shift+Enter` | 在输入框中换行。 |
| `Ctrl+O` | 展开或折叠最近一条工具调用结果（显示完整参数与输出）。 |
| `Ctrl+T` | 切换任务侧边栏。 |
| `PgUp` / `PgDn` | 滚动历史消息。 |
| 鼠标滚轮 | 滚动历史消息（需终端支持鼠标事件）。 |
| `Esc` | 关闭面板、取消当前操作或退出。 |

> 以下功能当前通过斜杠命令实现，暂无默认快捷键：会话选择器（`/sessions`）、扩展面板（`/extensions` 或 `/mcp`）、重试（`/retry`）、清空（`/new`）。

### 6.3 斜杠命令

在输入框中输入以下命令可实现快捷操作：

| 命令 | 作用 |
|---|---|
| `/mcp` | 管理已配置的 MCP 服务器：查看状态、重连、关闭。 |
| `/modes` | 切换当前 agent 模式。 |
| `/tasks` | 切换任务侧边栏。 |
| `/tools` | 展开或折叠所有工具结果。 |
| `/help` | 列出所有可用命令。 |

### 6.4 查看工具结果

当 assistant 调用工具时，TUI 会显示工具调用卡片。按 `Ctrl+O` 可展开查看：

- 工具名称与 call ID。
- 传入参数。
- 执行输出或错误信息。
- 执行耗时。

### 6.5 权限确认界面

当工具调用命中 `ask` 规则时，TUI 会弹出确认框，提供三个选项：

- **once**：仅允许本次调用。
- **project**：允许该规则，并写入 `<repo>/.lcoder/permissions.yaml`。
- **global**：允许该规则，并写入 `~/.lcoder/permissions/global.yaml`。

### 6.6 会话选择器

输入 `/sessions` 打开会话选择器，可：

- 查看当前工作区的所有会话。
- 按 `Enter` 切换到选中会话。
- 按 `Esc` 取消。

### 6.7 扩展面板

输入 `/extensions` 或 `/mcp` 打开扩展面板，显示：

- 已配置的 HTTP 工具列表。
- 已连接的 MCP 服务器及其工具列表。
- 服务器连接状态。

---

## 7. Agent 模式（Modes）

模式是一组针对特定任务优化的 system prompt 与行为配置。Lcoder 启动时会加载内置模式，以及 `<repo>/.lcoder/modes/` 和 `~/.lcoder/modes/` 下的自定义模式。

### 7.1 内置模式

| 模式 | 用途 |
|---|---|
| `code` | 默认模式，适合日常编码、重构、调试。 |
| `plan` | 设计模式，适合架构设计、方案讨论、任务拆解。 |
| `explore` | 探索模式，适合阅读陌生代码库、梳理调用链。 |
| `review` | 评审模式，适合对指定文件或提交进行代码评审。 |
| `test` | 测试模式，适合编写、分析、运行单元测试。 |

### 7.2 列出可用模式

```bash
./lcoder modes
```

输出示例：

```
- code: General coding assistant
- plan: Architecture and planning
- explore: Codebase exploration
- review: Code review
- test: Testing assistant
```

### 7.3 指定模式运行

```bash
./lcoder --mode plan -p "设计一个订单系统的数据库表结构"
./lcoder --mode review -p "请 review pkg/session/store.go"
./lcoder --mode explore
```

未指定时，默认模式为 `code`。

### 7.4 自定义模式

自定义模式以 YAML 文件形式存在，放在以下目录：

- `<repo>/.lcoder/modes/`
- `~/.lcoder/modes/`

每个模式是一个独立的 `.yaml` 文件，如 `review.yaml`。启动时由 `agent.ModeManager` 加载。

更多细节请参考 `DEVELOPER_GUIDE.md`。

---

## 8. 技能（Skills）

技能是按需加载的 Markdown 指令包，用于在特定场景下给 agent 补充指令、模板或背景知识。启动时只有每个技能的 `name + description` 进入 system prompt（catalog）；完整正文在激活时才加载。

### 8.1 技能文件位置

Lcoder 会从以下路径加载技能：

- `<repo>/.lcoder/skills/<name>/SKILL.md`
- `~/.lcoder/skills/<name>/SKILL.md`

一个技能包就是一个目录，里面至少包含 `SKILL.md`。多个相关文件也可以放在同一目录下。

### 8.2 列出已发现的技能

```bash
./lcoder skills
```

输出示例：

```
- security-review: Perform security-focused code review
- commit-message: Generate conventional commit messages
```

### 8.3 技能的激活方式

**模型自主激活（默认）**：system prompt 中的 catalog 列出了所有技能的名称与用途。模型判断用户请求与某个技能匹配时，会调用 `use_skill` 工具，技能正文作为该工具的返回结果进入对话，随轮次自然流动。

**手动触发**：在单次对话中，也可以用 `/skill:name` 语法强制激活：

```bash
./lcoder -p "/skill:security-review 请 review pkg/auth/jwt.go"
```

在 TUI 中，也可以在输入框中输入 `/skill:security-review` 触发。手动触发会把技能正文折叠进该条用户消息，与模型自主激活看到的内容一致。

### 8.4 用 allowed_tools 约束工具面

技能可以在 frontmatter 中声明 `allowed_tools`，限制激活期间可调用的工具：

```markdown
---
name: security-review
description: Review code for security vulnerabilities
allowed_tools:
  - read
  - grep
  - ls
  - find
---
```

激活后，对名单外工具的调用会在执行期被拒绝（`use_skill` 本身始终可用，模型可以随时切换到另一个技能；激活未声明 `allowed_tools` 的技能即解除限制）。限制只存在于当前进程内，不写入检查点。

### 8.5 示例技能

项目自带一个示例技能：

```
configs/skills/security-review/
├── SKILL.md
└── ...
```

你可以把它复制到 `~/.lcoder/skills/` 或项目本地的 `.lcoder/skills/` 下进行尝试。

---

## 9. 会话与分支

### 9.1 会话存储位置

会话以 JSONL 格式存储在：

```
~/.lcoder/sessions/<project-hash>/<session-id>.jsonl
```

其中 `<project-hash>` 由当前工作目录生成，确保同一项目下的会话归在一起。

### 9.2 会话文件结构

每条消息都是一行 JSON，包含：

- `id`：消息唯一 ID。
- `parent_id`：父消息 ID，用于表示分支关系。
- `role`：`user`、`assistant` 或 `tool`。
- `content`：消息内容。

通过 `parent_id`，单个 JSONL 文件即可表示一棵消息树，支持从任意节点分叉。

### 9.3 列出会话

```bash
./lcoder sessions
```

输出包含会话 ID 与创建时间：

```
abc123  2026-07-10 14:32
def456  2026-07-11 09:15
```

### 9.4 恢复会话

```bash
./lcoder -c                                    # 继续最近一次会话
./lcoder --session abc123 -p "继续昨天的工作"    # 恢复指定会话并发送新消息
./lcoder --session abc123                      # 在 TUI 中恢复指定会话
```

### 9.5 分叉与重试

Lcoder 的会话数据通过 `parent_id` 支持分支。当前 TUI 中尚未提供默认快捷键用于分叉；你可以通过编辑会话 JSONL 文件手动创建分支，或通过 `pkg/session` 包编程调用 `Fork`。

输入 `/retry` 可重试最近一条 assistant 消息。

### 9.6 备份与清理

会话文件是纯文本 JSONL，可直接复制备份：

```bash
cp ~/.lcoder/sessions/<project-hash>/<session-id>.jsonl ./session-backup.jsonl
```

如需清理旧会话，直接删除对应 JSONL 文件即可。

---

## 10. 安全与权限

Lcoder 默认以最小权限运行。所有可能破坏代码、文件或系统的工具在首次调用时都会请求确认。

### 10.1 默认权限规则

Lcoder 内置默认规则倾向于最小权限，但 `configs/lcoder.yaml` 示例将 `write` 和 `edit` 设为 `ask`。建议首次使用时复制 `configs/lcoder.yaml` 作为起点，否则内置默认可能允许更多操作。

示例规则（来自 `configs/lcoder.yaml`）：

```yaml
permissions:
  rules:
    read:
      "*": allow
    ls:
      "*": allow
    grep:
      "*": allow
    find:
      "*": allow
    write:
      "*": ask
    edit:
      "*": ask
    bash:
      "*": ask
      # 只读 / 安全命令无需提示
      "ls": allow
      "ls *": allow
      "pwd": allow
      "echo *": allow
      # 危险命令永远拒绝
      "rm -rf /": deny
      "sudo *": deny
```

### 10.2 规则动作

每条规则可以设置为：

- `allow`：无需确认直接执行。
- `ask`：每次调用都需要确认。
- `deny`：永远拒绝执行。

### 10.3 glob 匹配与优先级

规则使用 glob 模式匹配命令或路径。更具体的模式优先于更通用的模式。

例如：

```yaml
bash:
  "*": ask
  "git status": allow
  "rm -rf /": deny
```

- `git status` 匹配 `allow`。
- `rm -rf /` 匹配 `deny`。
- 其他所有命令匹配 `ask`。

### 10.4 交互式批准级别

当弹出确认框时，你可以选择：

- **once**：仅允许本次调用，不写入任何规则文件。
- **project**：将规则写入 `<repo>/.lcoder/permissions.yaml`，仅对当前项目生效。
- **global**：将规则写入 `~/.lcoder/permissions/global.yaml`，对所有项目生效。

### 10.5 `--unsafe` 模式

```bash
./lcoder --unsafe -p "执行某个危险操作"
```

`--unsafe` 会绕过权限引擎，但以下极端危险命令仍会被拦截（以权限引擎实际匹配模式为准）：

- `rm -rf /`
- `sudo *`
- `mkfs.*`
- `dd *`
- `reboot`、`shutdown`、`halt`
- fork bomb 等

### 10.6 审计日志

每次权限决策都会写入审计日志，路径为：

```
~/.lcoder/observability/sessions/<session-id>.jsonl
```

即使 `--unsafe` 生效时，也会记录 `unsafe-allow` 决策。

### 10.7 配置权限文件示例

项目级权限文件 `<repo>/.lcoder/permissions.yaml`：

```yaml
rules:
  bash:
    "go test ./...": allow
    "go build ./...": allow
  write:
    "*.go": ask
    "*.md": allow
```

全局权限文件 `~/.lcoder/permissions/global.yaml` 格式相同。

## 11. 扩展工具

Lcoder 支持三种方式扩展 agent 可用工具：

1. **内置工具**：由 Lcoder 自带，如 `read`、`write`、`edit`、`bash`、`memory` 等。其中 `subagent` 仅在配置中启用后才注册。
2. **HTTP 工具**：通过配置向任意 HTTP 端点暴露工具。
3. **进程外扩展**：以独立进程运行、通过 stdio JSON-RPC 与宿主通信的扩展，从 `~/.lcoder/extensions/`（全局）或 `.lcoder/extensions/`（项目级）自动发现，目录内需有 `extension.yaml` 清单。

> **注意**：`./lcoder install` 只是把扩展源码安装到 `~/.lcoder/extensions/`，不会自动注册。进程外扩展按上述目录自动发现，无需在 `tool_extensions` 中声明；HTTP 工具直接写 `http_tools` 即可生效。

### 启用子代理

```yaml
subagent:
  enabled: true
```

启用后 agent 获得 `subagent` 工具，可把自包含的任务委托给**同进程子代理**（一个拥有干净上下文的完整 agent 实例，不是子进程）：

```text
用 subagent 的 explore 类型梳理 pkg/agent 的调用链
```

**agent 类型（profile）**：内嵌 `coder`（编码）与 `explore`（只读研究）两种；自定义类型在 `~/.lcoder/agents/*.md` 或 `<项目>/.lcoder/agents/*.md` 中用 YAML frontmatter 定义（`name`/`description`/`mode`/`timeout`/`max_turns`/`summary_min_chars` 等），按名覆盖内嵌类型。

**使用形态**：

- 单个：`{agent, task}`；`task` 必须自包含（子代理看不到当前对话）
- 续跑：`{task, resume: "<agent_id>"}`——超时/失败的结果里会带 agent_id 和续跑提示，子代理的完整上下文从 journal 恢复
- 批量（swarm）：`{agent, prompt_template, items}`——模板里用 `{{item}}` 占位，每个 item 展开成一个子代理（至少 2 个、至多 128 个，展开后 prompt 必须互不相同；有界并发；必须是响应中唯一的工具调用）
- 后台：`run_in_background: true`，结果自动以提醒形式送达，无需轮询

### 注册扩展

`tool_extensions` 目前仅支持 `type: json`：`path` 指向一个 JSON 描述文件（定义 HTTP 工具的 `name`/`endpoint`/`parameters` 等，与 `http_tools` 等价）：

```yaml
tool_extensions:
  - name: weather
    type: json
    path: ~/.lcoder/tools/weather.json
```

进程外扩展不通过 `tool_extensions` 配置，而是从 `~/.lcoder/extensions/`（全局）或 `.lcoder/extensions/`（项目级）自动发现，设计详见 `docs/superpowers/specs/2026-07-24-extension-runtime-design.md`。

### 11.1 HTTP 工具

HTTP 工具会在 agent 需要时向指定端点发送 POST 请求，并把响应作为工具结果返回。

在 `~/.lcoder/config.yaml` 中配置：

```yaml
http_tools:
  - name: deploy
    endpoint: http://localhost:9001/deploy
    description: Deploy service to staging
    parameters:
      type: object
      properties:
        service:
          type: string
      required: [service]
    execution_mode: parallel
    headers:
      Authorization: Bearer ${DEPLOY_TOKEN}
```

字段说明：

| 字段 | 说明 |
|---|---|
| `name` | 工具在 agent 中的显示名称。 |
| `endpoint` | POST 请求目标地址。 |
| `description` | 工具描述，影响 LLM 何时调用它。 |
| `parameters` | JSON Schema 参数定义。 |
| `execution_mode` | `parallel` 或 `serial`，决定工具是否可以并行执行。 |
| `headers` | 自定义请求头，支持 `${VAR}` 环境变量插值。 |

环境变量插值：

```yaml
headers:
  Authorization: "Bearer ${DEPLOY_TOKEN}"
```

启动时，Lcoder 会读取 `DEPLOY_TOKEN` 环境变量并替换。

### 11.2 HTTP 工具端点约定

你的服务端需要接收 POST 请求，请求体为 JSON：

```json
{
  "tool_call_id": "call_xxx",
  "name": "deploy",
  "arguments": {"service": "api"},
  "context": {"cwd": "/path/to/project"}
}
```

响应体应为 tool 结果格式。最简单的方式是返回一段文本：

```json
{
  "content": [{"type": "text", "text": "Deployment started"}]
}
```

也支持 `details` 和 `terminate` 字段。

### 11.3 MCP 服务器

MCP（Model Context Protocol）是一种标准化协议，允许 Lcoder 接入外部工具服务器。Lcoder 支持三种传输方式：

- **stdio**：通过子进程标准输入输出通信，适合本地工具。
- **sse**：Server-Sent Events，适合远程服务器。
- **streamable-http**：Streamable HTTP，适合远程服务器。

配置示例：

```yaml
mcp_servers:
  - name: filesystem
    transport: stdio
    command: ["npx", "-y", "@modelcontextprotocol/server-filesystem", "."]
    env:
      NODE_ENV: production

  - name: remote-sse
    transport: sse
    url: http://localhost:3000
    headers:
      Authorization: Bearer ${REMOTE_MCP_TOKEN}
    timeout: 60

  - name: remote-http
    transport: streamable-http
    url: https://mcp.example.com/v1
    headers:
      Authorization: Bearer ${REMOTE_MCP_TOKEN}
    timeout: 60
```

字段说明：

| 字段 | 说明 |
|---|---|
| `name` | 服务器名称，MCP 工具会显示为 `{serverName}_{toolName}`。 |
| `transport` | 传输方式：`stdio`、`sse`、`streamable-http`。 |
| `command` | stdio 模式下启动服务器的命令。 |
| `url` | sse / streamable-http 模式下的服务器地址。 |
| `headers` | 连接时使用的 HTTP 头。 |
| `env` | stdio 模式下传递给子进程的环境变量。 |
| `timeout` | 连接超时秒数。 |

### 11.4 在 TUI 中管理 MCP

输入 `/mcp` 打开 MCP 管理界面，可查看：

- 每个服务器的连接状态。
- 服务器提供的工具列表。
- 重连或关闭指定服务器。

### 11.5 工具超时

对于耗时操作，Lcoder 允许 LLM 控制超时：

- `bash` 工具提供 `timeout` 参数，默认 **120 秒**。
- MCP 工具在服务器未自行定义时，暴露可选的 `timeout_seconds` 参数，默认 **120 秒**。

如果 LLM 未指定，则使用默认值。

---

## 12. 代码智能（MCP / codegraph）

Lcoder 不内置代码索引，而是通过 MCP 接入外部代码智能工具。推荐搭配 [codegraph](https://github.com/colbymchenry/codegraph)：它用 tree-sitter 将仓库解析为符号/关系图（SQLite + FTS5），并以 MCP server 形式暴露只读工具。

### 12.1 安装与建索引

1. 按 codegraph 项目 README 安装（自带 Node runtime 的独立二进制，无需安装 Node）。
2. 在仓库根目录执行一次 `codegraph init`，生成 `.codegraph/` 并建立全量索引。之后 `serve` 进程内的文件监听器会自动做增量更新（约 1 秒延迟）。

### 12.2 在 Lcoder 中注册

在 `~/.lcoder/config.yaml` 中配置：

```yaml
mcp_servers:
  - name: codegraph
    transport: stdio
    command: ["codegraph", "serve", "--mcp", "--path", "."]
    env:
      CODEGRAPH_NO_DAEMON: "1"   # 单进程直连，生命周期由 lcoder 管理
      CODEGRAPH_TELEMETRY: "0"   # 关闭匿名遥测
```

### 12.3 可用工具

连接后 agent 可使用 codegraph 暴露的只读工具（默认只开放 `codegraph_explore`，可用 `CODEGRAPH_MCP_TOOLS` 环境变量开放更多）：

| 工具 | 说明 |
|---|---|
| `codegraph_explore` | 自然语言/关键词 → 相关符号源码（带行号）、调用路径、影响面摘要 |
| `codegraph_search` | 符号名搜索，返回位置 |
| `codegraph_files` | 索引文件树 |

示例：

```text
用 codegraph_explore 查一下 NewClient 的调用方和相关代码
```

explore/plan/review 模式的 prompt 已引导 agent 在定位符号、调用链时优先使用这类工具，找不到时再回退到 grep/find。

---

## 13. 可观测性

Lcoder 会自动记录会话运行过程中的各类事件，便于后续分析成本、性能与行为。

### 13.1 数据存储位置

默认可观测数据写入：

```
~/.lcoder/observability/sessions/<session-id>.jsonl
```

每条记录是一个 JSON 对象，包含事件类型、时间戳、相关指标等。

### 13.2 查看会话统计

```bash
./lcoder stats <session-id>
```

输出示例：

```
turns: 12
input tokens: 34567
output tokens: 8901
total cost: $0.023456
```

### 13.3 查看可读 Trace

```bash
./lcoder trace <session-id>
```

会以人类可读的格式打印每一轮对话、工具调用和事件。

### 13.4 导出数据

```bash
# HTML 报告（默认）
./lcoder export <session-id>

# SQLite 数据库
./lcoder export <session-id> --format sqlite -o report.db

# Markdown 报告
./lcoder export <session-id> --format markdown -o report.md

# Prometheus 指标文本
./lcoder export <session-id> --format prometheus -o metrics.txt
```

### 13.5 Prometheus 指标端点

```bash
# 默认在 :9090 启动
./lcoder metrics

# 指定端口
./lcoder metrics 9091
```

暴露的指标包括：

- LLM 调用次数、input/output/total tokens、cache tokens、cost。
- 工具执行次数、耗时、错误数。
- 每轮耗时。
- 会话总耗时。

### 13.6 配置可观测性

可观测性配置位于 `~/.lcoder/observability.yaml`（或 `configs/observability.yaml` 示例）。可以配置采样率、审计日志、上下文快照等。

示例见 `configs/observability.yaml`。

---

## 14. 故障排查

### 14.1 启动时提示模型不支持工具

```
warning: model "xxx" does not declare the "tools" capability; tool calls may fail
```

这表示模型目录中没有该模型的能力信息，或者模型本身不支持工具调用。建议：

- 换用支持工具调用的模型，如 `gpt-4o`、`claude-3-5-sonnet`、`deepseek-chat` 等。
- 在 `models.yaml` 中手动声明该模型的 `capabilities`。

### 14.2 上下文窗口回退警告

```
warning: 未能自动获取模型 "xxx" 的上下文窗口,回退默认 128000
```

表示 Lcoder 无法从模型目录获取窗口大小，已使用默认值。建议：

- 检查 `provider` 是否配置正确。
- 在 `models.yaml` 中手动声明 `context_window`。

### 14.3 MCP 服务器连接失败

- 检查命令是否正确安装（stdio 模式）。
- 检查 URL 和端口是否可访问（sse / streamable-http 模式）。
- 检查认证头是否正确。
- 在 TUI 中用 `/mcp` 查看详细错误。

### 14.4 权限被拒绝

如果某个工具始终无法执行：

- 检查 `~/.lcoder/config.yaml` 中的 `permissions.rules`。
- 检查项目级 `<repo>/.lcoder/permissions.yaml`。
- 检查全局 `~/.lcoder/permissions/global.yaml`。
- 更具体的 glob 规则会覆盖通用规则。

### 14.5 配置验证失败

```
invalid config: ...
```

启动时会调用 `cfg.Validate()` 检查配置。常见错误：

- 必填字段缺失。
- 未知的 provider。
- 规则语法错误。

### 14.6 TUI 显示异常

- 确保终端支持 256 色或真彩色。
- 尝试设置 `TERM=xterm-256color`。
- Windows 用户建议使用 Windows Terminal 或最新版 PowerShell。

### 14.7 会话没有正确恢复

- 确认 `~/.lcoder/sessions/` 下存在对应文件。
- 检查是否使用了正确的工作目录（project hash 与工作目录相关）。
- 如果 checkpoint 恢复失败，Lcoder 会回退到仅使用会话消息。

---

## 15. 配置字段完整参考

以下是对 `configs/lcoder.yaml` 中所有配置字段的详细说明。

### 15.1 顶层字段

```yaml
provider: openai
model: gpt-4o-mini
# thinking: medium
# models_source: https://models.dev/api.json
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `provider` | string | 默认 LLM provider。 |
| `model` | string | 默认模型 ID。 |
| `thinking` | string | 思考模式：`off` / `on` / 模型声明的档位（如 `low`/`medium`/`high`）。缺省不发 thinking 字段；模型已声明档位时未声明的档位回退为 `on` 并输出 warning；always-thinking 模型（thinking 不可关闭，如 gpt-5）配置 `off` 会被忽略并输出 warning；模型未声明档位列表时自定义档位原样透传。 |
| `models_source` | string | 自定义 models.dev 风格模型目录 URL（如内网 registry）。环境变量 `LCODER_MODELS_SOURCE` 优先。 |

### 15.2 TUI 配置

```yaml
tui:
  theme: dark   # dark 或 light
```

### 15.3 Provider 连接层

```yaml
providers:
  moonshot:
    base_url: "https://api.moonshot.cn/v1"
    api_key: "{env:MOONSHOT_API_KEY}"
  myrelay:
    route: openai
    base_url: "https://api.relay.com/v1"
    api_key: "{env:RELAY_KEY}"
    headers:
      X-Title: lcoder
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `base_url` | string | 自定义 API 基础地址。 |
| `api_key` | string | API key，支持 `{env:VAR}` 语法。 |
| `route` | string | 协议路由，如 `openai`，用于 OpenAI 兼容端点。 |
| `headers` | map | 自定义 HTTP 头。 |

### 15.4 上下文管理

```yaml
context:
  # max_tokens: 128000
  # target_tokens: 120000
  # reserve_output: 8192
  static_ratio: 60
  min_recent: 10
  keep_recent_tokens: 20000
  compact_threshold: 0.9
  cache_hint_policy: default
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `max_tokens` | int | 强制覆盖上下文窗口大小。 |
| `target_tokens` | int | 目标 token 使用量。 |
| `reserve_output` | int | 为模型输出预留的 token 数。 |
| `static_ratio` | int | 静态/稳定块的目标占比百分比。 |
| `min_recent` | int | 压缩时至少保留的最近消息数。 |
| `keep_recent_tokens` | int | 压缩时保留尾部的 token 预算。 |
| `compact_threshold` | float | 使用量达到 target_tokens 的多少比例时触发压缩。 |
| `cache_hint_policy` | string | cache 断点策略：`default`、`aggressive`、`none`。 |

### 15.5 记忆

```yaml
memory:
  enabled: true
  dynamic_recall: true
  recall_max_tokens: 1024
  recall_min_score: 0.1
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `enabled` | bool | 是否启用持久化记忆。 |
| `dynamic_recall` | bool | 是否每轮根据相关性召回记忆。 |
| `recall_max_tokens` | int | 每轮召回记忆的 token 预算。 |
| `recall_min_score` | float | 召回记忆的最小相关度分数（0..1）。 |

### 15.6 代码索引

见第 12 章。

### 15.7 权限

见第 10 章。

### 15.8 HTTP 工具与 MCP 服务器

见第 11 章。

### 15.9 Hooks

```yaml
hooks:
  audit:
    enabled: true
  sensitive_file_check:
    enabled: true
    patterns: ["*.env", "*.key", "*.pem"]
  bash_denylist:
    enabled: true
    patterns: ["rm -rf /", "mkfs.*"]
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `audit` | map | 启用审计日志。 |
| `sensitive_file_check` | map | 检查敏感文件访问。 |
| `bash_denylist` | map | bash 命令黑名单。 |

### 15.10 扩展

```yaml
extensions:
  disabled: ["noisy"]      # 按名禁用已发现的进程外扩展
  hook_timeout_ms: 5000    # 扩展 hook 超时
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `extensions.disabled` | list | 按名禁用的进程外扩展。 |
| `extensions.hook_timeout_ms` | int | 扩展 hook 超时（毫秒）。 |

---

## 16. 附录：环境变量与文件路径速查

### 16.1 常用环境变量

| 变量 | 说明 |
|---|---|
| `OPENAI_API_KEY` | OpenAI API key。 |
| `ANTHROPIC_API_KEY` | Anthropic API key。 |
| `DEEPSEEK_API_KEY` | DeepSeek API key。 |
| `MOONSHOT_API_KEY` | Moonshot（Kimi）API key。 |
| `DASHSCOPE_API_KEY` | DashScope（通义千问）API key。 |

### 16.2 文件路径速查

| 路径 | 说明 |
|---|---|
| `~/.lcoder/config.yaml` | 主配置文件。 |
| `~/.lcoder/credentials.yaml` | API key 凭据文件（权限 0600）。 |
| `~/.lcoder/observability.yaml` | 可观测性配置文件。 |
| `~/.lcoder/permissions/global.yaml` | 全局权限规则。 |
| `~/.lcoder/sessions/<project-hash>/<session-id>.jsonl` | 会话存储。 |
| `~/.lcoder/observability/sessions/<session-id>.jsonl` | 可观测数据。 |
| `~/.lcoder/skills/<name>/SKILL.md` | 全局技能。 |
| `~/.lcoder/modes/` | 全局模式目录。 |
| `~/.lcoder/memory/{MEMORY,USER}.md` | 全局记忆文件。 |
| `~/.lcoder/extensions/` | 已安装的进程外扩展。 |
| `<repo>/AGENTS.md` | 项目级 agent 说明（从当前目录向上搜索到 git 根）。 |
| `<repo>/CLAUDE.md` | 项目级 Claude Code 原则（搜索方式同上）。 |
| `<repo>/LCODER.md` | 项目级 Lcoder 说明（搜索方式同上）。 |
| `<repo>/.lcoder/modes/` | 项目级模式目录。 |
| `<repo>/.lcoder/skills/<name>/SKILL.md` | 项目级技能。 |
| `<repo>/.lcoder/memory/{MEMORY,USER}.md` | 项目级记忆文件。 |
| `<repo>/.lcoder/permissions.yaml` | 项目级权限规则。 |

### 16.3 常用命令速查

```bash
# 构建
go build -o lcoder ./cmd/lcoder

# 运行
./lcoder -p "提示"
./lcoder
./lcoder -c

# 管理
./lcoder models
./lcoder skills
./lcoder sessions
./lcoder modes

# 可观测性
./lcoder stats <id>
./lcoder trace <id>
./lcoder export <id> --format html
./lcoder metrics 9090

# 扩展
./lcoder install SOURCE --name NAME
./lcoder uninstall NAME
./lcoder list-extensions
./lcoder update NAME
```

---

> 本手册基于 Lcoder 当前实现编写。如有功能变动，请以源码和 `README.md` 为准。
