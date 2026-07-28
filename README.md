# Lcoder

[English Version](README_EN.md)

一个极简、可扩展的 SWE agent 运行时框架。

- **核心语言**：Go
- **LLM 引擎**：进程内 Go 实现（手写 OpenAI 兼容与 Anthropic 的 HTTP+SSE 适配器）
- **扩展工具**：HTTP 工具与 MCP 服务器（stdio、SSE、Streamable HTTP）
- **交互界面**：基于 `charmbracelet/bubbletea` 的终端 TUI
- **会话存储**：JSONL，支持分支（`parent_id`）

## 快速开始

### 1. 编译 Go CLI

```bash
go build -o lcoder ./cmd/lcoder
```

### 2. 配置

```bash
mkdir -p ~/.lcoder
cp configs/lcoder.yaml ~/.lcoder/config.yaml
# 编辑 ~/.lcoder/config.yaml，并通过环境变量设置 API key：
# OPENAI_API_KEY, ANTHROPIC_API_KEY, DEEPSEEK_API_KEY
```

### 3. 运行

单次对话：

```bash
./lcoder -p "列出当前目录的文件"
# 或直接使用位置参数
./lcoder "列出当前目录的文件"
```

继续会话：

```bash
./lcoder -c                              # 继续最近一次会话
./lcoder --session <id> -p "继续"        # 恢复指定会话
```

交互式 TUI：

```bash
./lcoder          # 或 ./lcoder tui
./lcoder tui --session <id>
```

TUI 快捷键：
- `Enter` 发送消息
- `Shift+Enter` 换行
- `Ctrl+O` 展开/折叠工具调用结果（完整输出 + 参数）
- `Ctrl+T` 切换任务侧边栏
- `Ctrl+M` 切换扩展面板（HTTP 工具 / MCP 服务器）
- `Ctrl+S` 会话选择器
- `Ctrl+B` 从最近一条 assistant 消息分叉
- `Ctrl+R` 重试最近一条 assistant 消息
- `Ctrl+L` 清空聊天
- `PgUp/PgDn` 或鼠标滚轮滚动历史
- `Ctrl+C` / `Esc` 退出

输入时的斜杠命令：
- `/mcp` 管理已配置的 MCP 服务器（重连 / 关闭）
- `/modes` 切换 agent 模式
- `/tasks` 切换任务侧边栏
- `/tools` 展开/折叠所有工具结果
- `/help` 列出所有命令

列出模型：

```bash
./lcoder models
```

列出 agent 模式（默认模式已嵌入，可在任意目录使用）：

```bash
./lcoder modes
```

使用指定模式运行：

```bash
./lcoder --mode plan -p "设计认证模块"
./lcoder --mode review -p "Review pkg/agent/loop.go"
```

## 项目上下文

Lcoder 会从当前目录向上遍历到文件系统根目录，加载遇到的 `AGENTS.md` 和 `CLAUDE.md`，并追加到 system prompt。

它也会从 `.lcoder/skills/<name>/SKILL.md` 或 `~/.lcoder/skills/<name>/SKILL.md` 加载 Markdown 技能并注入 system prompt。

## 技能

技能是位于 `.lcoder/skills/<name>/SKILL.md` 或 `~/.lcoder/skills/<name>/SKILL.md` 的 Markdown 包。

列出已发现的技能：

```bash
./lcoder skills
```

`configs/skills/security-review/` 提供了一个示例技能。

## 会话

会话以 JSONL 形式存储在 `~/.lcoder/sessions/<project-hash>/`。每条消息记录 `parent_id`，因此单个会话文件即可表示一棵树的分支。

```bash
./lcoder sessions                                  # 列出会话
./lcoder -c                                        # 继续最近一次会话
./lcoder --session <id>                            # 恢复会话
./lcoder fork --session <id> --message <msg-id>    # 从某条消息分叉
./lcoder clone --session <id>                      # 克隆当前分支
```

## 安全默认值

Lcoder 默认以最小权限运行。具有破坏性的工具初始为 "ask" 模式，每次调用都需要确认，也可以按项目或全局放行。

- `write` 与 `edit` 默认对每个路径都**询问**。
- `bash` 默认**询问**。内置少量白名单命令（如 `ls`、`pwd`、`echo`、`git status`、`git log`、`git diff`、`git branch`）无需提示即可执行。
- 交互式批准时可选择：
  - **once** — 仅允许本次调用
  - **project** — 记到 `<repo>/.lcoder/permissions.yaml`
  - **global** — 记到 `~/.lcoder/permissions/global.yaml`
- 使用 `--unsafe` 可绕过权限引擎；但 `rm -rf /` 等极端危险命令仍需要确认。
- 每次权限决策都会写入审计日志，包括 `--unsafe` 生效时的 `unsafe-allow`。

规则使用 glob 匹配，更具体的模式优先。示例见 `configs/lcoder.yaml`。

## 可观测性

Lcoder 将可观测数据写入 `~/.lcoder/observability/sessions/<session-id>.jsonl`。

```bash
./lcoder stats <id>              # 会话统计
./lcoder trace <id>              # 人类可读的 trace
./lcoder export <id>             # 导出为 HTML（默认）
./lcoder export <id> --format sqlite -o report.db
./lcoder export <id> --format prometheus -o metrics.txt
./lcoder metrics                 # 在 :9090 启动 Prometheus 指标端点
./lcoder metrics 9091            # 在 :9091 启动
```

观测指标包括：

- LLM 调用次数、input/output/total tokens、cache tokens、cost
- 工具执行次数、耗时、错误数
- 每轮耗时
- 会话总耗时

## 扩展工具

Lcoder 支持两种扩展机制：

1. **HTTP 工具** — 向本地或远程端点发送 POST 请求。
2. **MCP 服务器** — 通过 stdio、SSE 或 Streamable HTTP 连接 Model Context Protocol 服务器。

示例 `~/.lcoder/config.yaml`：

```yaml
http_tools:
  - name: deploy
    endpoint: http://localhost:9001/deploy
    description: Deploy to staging
    parameters:
      type: object
      properties:
        service: { type: string }
      required: [service]
    execution_mode: parallel
    headers:
      Authorization: Bearer ${DEPLOY_TOKEN}

mcp_servers:
  - name: filesystem
    transport: stdio
    command: ["npx", "-y", "@modelcontextprotocol/server-filesystem", "."]

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

`transport` 字段必填。MCP 工具在 agent 工具列表中显示为 `{serverName}_{toolName}`。

TUI 中可用 `/mcp` 查看并重连服务器。

## 工具超时

耗时工具支持由 LLM 控制的超时：

- `bash` 提供 `timeout` 参数（秒，默认 **120**）。
- MCP 工具在服务器未自行定义时，暴露可选的 `timeout_seconds` 参数（默认 **120**）。
- 若 LLM 未指定参数，则使用默认值。

## 代码智能（MCP / codegraph）

Lcoder 不内置代码索引，而是通过 MCP 接入外部代码智能工具。推荐搭配 [codegraph](https://github.com/colbymchenry/codegraph)：它用 tree-sitter 把仓库解析成符号/关系图（SQLite + FTS5），并通过 MCP 暴露 `codegraph_explore`（自然语言/关键词 → 相关符号源码、调用路径、影响面）、`codegraph_search`、`codegraph_files` 等只读工具。

使用方式：

1. 安装 codegraph（自带 Node runtime 的独立二进制，见项目 README）。
2. 在仓库根目录执行一次 `codegraph init` 建立索引（之后其 serve 进程内的 watcher 会自动增量更新）。
3. 在 `~/.lcoder/config.yaml` 中注册 MCP 服务器：

```yaml
mcp_servers:
  - name: codegraph
    transport: stdio
    command: ["codegraph", "serve", "--mcp", "--path", "."]
    env:
      CODEGRAPH_NO_DAEMON: "1"   # 单进程直连，生命周期由 lcoder 管理
      CODEGRAPH_TELEMETRY: "0"   # 关闭匿名遥测
```

连接后，agent 在探索符号、调用链、影响面时会优先使用这些 MCP 工具（explore/plan/review 模式的 prompt 已按此引导）。

## SWE-bench Lite 评测

`eval/swe-bench-lite/` 提供了面向 SWE-bench Lite 的专用评测框架。它在 Docker 容器中运行 Lcoder，测量初次与反馈后的修复率，并生成 HTML/Markdown 汇总报告，包括 token 消耗、缓存命中率、工具链路、核心模块性能等指标。

详见 `eval/swe-bench-lite/README.md`（中文）或 `eval/swe-bench-lite/README_EN.md`（英文）。

## 架构

```
cmd/lcoder/main.go
 └─ prepareAgent: 配置 → LLM 客户端 → 工具注册表 → MCP 注册表
                 → 会话存储 → 可观测性 → 模式管理器 → 上下文管理器 → Agent
 └─ runRoot: 单次 / JSON / TUI 模式分发，SIGINT/SIGTERM 时写 ReasonCrash checkpoint

pkg/agent
 ├─ loop.go            编排多轮对话：drain steering → compact → 流式生成 → 执行工具 → 持久化 checkpoint
 ├─ streamer.go        构建 turn 请求、流式接收 LLM 事件、组装 assistant 消息
 ├─ executor.go        验证、权限检查、执行工具调用；负责 deferred tool 提升
 └─ state.go           运行时状态、turn 计数、steering/follow-up 队列、abort

pkg/contextmgr
 └─ Manager            将对话组织为 system/mode/skills/project_docs/recent 等 block
                       BuildTurnRequest 在 TokenBudget 内选块、计算 cache 断点、注入临时提醒、 MaybeCompactLeveled

pkg/llm
 ├─ engine             路由与重试
 ├─ catalog            模型目录、窗口与能力发现
 ├─ provider           OpenAI 兼容 / Anthropic 的 HTTP+SSE 适配器
 └─ client.go          面向 agent 的客户端门面

pkg/tools
 └─ Registry           收集工具定义；内置工具在 pkg/tools/builtin，HTTP/MCP 工具从配置注册；支持 deferred 加载

pkg/events            事件总线：TurnStart/End、MessageStart/End、ToolExecutionStart/End、CompactionCommitted 等
pkg/session           JSONL 会话存储，基于 parent_id 重建活动分支
pkg/checkpoint        轻量级运行时快照（模式、模型、turn、上下文预算/策略、steering 队列等），不存完整消息
pkg/tui               基于 Bubble Tea 的终端 UI，订阅同一事件总线并处理权限 Ask
pkg/config            koanf 加载 ~/.lcoder/config.yaml，支持环境变量覆盖
```

更详细的项目约定见 `.claude/CLAUDE.md`，设计笔记与报告见 `docs/`。

## 许可证

MIT
