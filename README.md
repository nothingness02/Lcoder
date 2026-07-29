# Lcoder

[English Version](README_EN.md)

**Lcoder** 是一个面向软件工程的 AI coding agent —— 纯 Go 实现、单二进制、零外部依赖。

## 为什么选择 Lcoder

| 特性 | Lcoder | 其他 agent |
|------|:---:|:---:|
| **路径安全守卫** | ✅ 敏感文件检测 + workspace 边界 | 多数无此机制 |
| **单二进制部署** | ✅ `go build` 即可，无需 Node/Python | 多数需要 Node.js 运行时 |
| **多模式 Agent** | ✅ code / plan / explore / review | 多数仅单一模式 |
| **会话分支** | ✅ 任意消息分叉、克隆、重试 | 少数支持 |
| **检查点恢复** | ✅ 崩溃自动保存，会话可精确恢复 | 多数不支持 |
| **上下文压缩** | ✅ 分层压缩 + 缓存命中策略 | 多数仅简单截断 |
| **权限引擎** | ✅ 四级决策链 + 审核日志 | 参差不齐 |
| **可观测性** | ✅ Prometheus / HTML / SQLite / Markdown | 多数无内建支持 |
| **子 Agent 集群** | ✅ 并行子 agent + swarm 模式 | 少数支持 |
| **延迟工具加载** | ✅ tool_search 按需展开 | 多数全量下发 schema |
| **扩展系统** | ✅ MCP + HTTP 工具 + 扩展桥 | MCP 支持参差不齐 |

## 快速开始

```bash
# 编译（无需任何运行时依赖）
go build -o lcoder ./cmd/lcoder

# 配置
mkdir -p ~/.lcoder
cp configs/lcoder.yaml ~/.lcoder/config.yaml

# 设置 API key（任选其一）
export OPENAI_API_KEY="sk-..."       # OpenAI 兼容
export ANTHROPIC_API_KEY="sk-ant-..." # Anthropic
export DEEPSEEK_API_KEY="sk-..."     # DeepSeek

# 开始使用
./lcoder "列出当前项目结构"
./lcoder                   # 交互式 TUI
```

## 核心能力

### 🛡️ 路径安全

对标 Kimi Code 的安全架构。所有文件工具（read/write/edit/ls/grep/find）在执行前统一经过路径安全守卫：

- **敏感文件阻断**：`.env`、SSH 私钥、credentials 永远不可被 agent 读写
- **workspace 边界**：`../` 相对路径逃逸被拒绝，外部访问必须显式使用绝对路径
- **权限之前执行**：守卫在权限引擎之前运行，敏感操作不打扰用户

### 🧠 多模式 Agent

四种内建模式，每种有独立的 system prompt 和工具约束：

| 模式 | 用途 | 工具限制 |
|------|------|:---:|
| `code` | 日常编码实现 | 全工具 |
| `plan` | 设计方案、分析需求 | 仅只读 + todo_write |
| `explore` | 代码库探索和理解 | 仅只读 |
| `review` | 代码审查 | 仅只读 |

```bash
./lcoder --mode plan "设计用户认证方案"
./lcoder --mode review "审查 pkg/agent/loop.go"
```

支持自定义模式（`.lcoder/modes/*.yaml`），可配置独立 provider/model、工具规则、退出审批。

### 📦 技能系统

将领域知识封装为可复用的 Markdown 技能包：

```bash
.lcoder/skills/
├── security-review/SKILL.md   # 安全审查流程
├── api-design/SKILL.md        # API 设计规范
└── db-migration/SKILL.md      # 数据库迁移指南
```

技能可以有 `allowed_tools` 约束，激活后临时限制 agent 工具面。

### 🔀 会话分支

每条消息记录 `parent_id`，一个 JSONL 文件即一棵对话树：

```bash
./lcoder fork --session <id> --message <msg-id>   # 从任意消息分叉
./lcoder clone --session <id>                     # 克隆当前分支
./lcoder -c                                       # 继续最近会话
```

### 💾 检查点

崩溃时自动保存轻量级运行快照（模式、模型、turn 计数、上下文预算等）。恢复时精确还原状态，无需重放消息历史。

### 📊 上下文管理

分级上下文组织 + 智能压缩：

```
[system prompt] [mode prompt] [skills] [project docs] [recent messages]
                                                          ↑
                                          TokenBudget 内动态分配
```

- 分层压缩：system / project 为稳定层，recent 为滑动窗口
- `CompactThreshold` 触发主动压缩，而非等到预算耗尽
- `CacheHintPolicy` 与 Anthropic prompt cache 深度集成
- 支持 `DropThreshold` 在极端压力下丢弃旧消息

### 🔧 延迟工具加载

当工具数量庞大时（大量 MCP 工具），仅下发核心工具完整 schema + `tool_search`：

```yaml
context:
  deferred_tools: true
  core_tools: ["read", "write", "edit", "bash", "ls", "grep", "find"]
```

模型通过 `tool_search` 按需查找并用 `tool_activate` 加载。节省首 token 延迟和 provider cache 前缀。

### 🐝 子 Agent 集群

```json
{
  "agent": "code",
  "items": [
    "修复 handler.go 的 bug",
    "给 service.go 加单元测试",
    "更新 api 文档"
  ]
}
```

- **并行模式**：多个子 agent 并发执行，结果聚合到父 agent
- **Swarm 模式**：批次独占，适合大规模并行任务
- 子 agent 继承父 agent 的权限和会话

### 📈 可观测性

多格式导出，覆盖 LLM 调用、工具执行、延迟等全套指标：

```bash
./lcoder stats <id>                           # 会话统计
./lcoder trace <id>                           # 可读 trace
./lcoder export <id>                          # HTML 报告
./lcoder export <id> --format sqlite -o.db    # SQLite 数据库
./lcoder export <id> --format prometheus -o.txt
./lcoder metrics                              # Prometheus 端点 (:9090)
```

### 🔐 权限引擎

四级决策链，每次决策都有审计日志：

```
guard policies → unsafe → deny rules → session approval → user rules → dangerous-default → fallback
```

- write / edit / bash 默认 ask，白名单内命令免审批
- 审批作用域：once / project（记入 `.lcoder/permissions.yaml`）/ global
- 每条权限决策写入审计日志，包括 unsafe 模式标记
- Glob 模式匹配，更具体的规则优先

### 🔌 扩展系统

三种扩展机制协同工作：

**MCP 服务器**（stdio / SSE / Streamable HTTP）：
```yaml
mcp_servers:
  - name: codegraph
    transport: stdio
    command: ["codegraph", "serve", "--mcp", "--path", "."]
```

**HTTP 工具**：
```yaml
http_tools:
  - name: deploy
    endpoint: http://localhost:9001/deploy
    parameters:
      type: object
      properties:
        service: { type: string }
```

**扩展桥**（子进程通信，实现自定义工具）：
```yaml
extensions:
  bridges:
    - name: my-bridge
      command: ["./my-tool", "serve"]
```

### ⏱️ 工具超时

耗时工具支持 LLM 可控超时，防止挂起：

- `bash`：`timeout` 参数（默认 120 秒）
- MCP 工具：`timeout_seconds` 参数（默认 120 秒）

## 交互界面

```bash
./lcoder          # 交互式 TUI
./lcoder -p "..." # 单次对话
./lcoder -c       # 继续会话
```

TUI 快捷键：

| 快捷键 | 功能 |
|--------|------|
| `Enter` | 发送消息 |
| `Shift+Enter` | 换行 |
| `Ctrl+O` | 展开/折叠工具输出 |
| `Ctrl+T` | 任务侧边栏 |
| `Ctrl+B` | 从最后一条消息分叉 |
| `Ctrl+R` | 重试最后一条消息 |
| `Ctrl+S` | 会话选择器 |
| `Ctrl+L` | 清空聊天 |
| `Ctrl+M` | 扩展面板 |
| `PgUp/PgDn` | 滚动 |

斜杠命令：`/mcp` `/modes` `/tasks` `/tools` `/help`

## 架构

```
cmd/lcoder/main.go
 └─ prepareAgent   配置 → LLM → 工具/MCP注册 → 会话存储 → 可观测性 → 模式 → 上下文 → Agent
 └─ runRoot        单次/JSON/TUI 分发，崩溃时写 checkpooint

pkg/agent          多轮编排：steering → compact → streaming → tool exec → checkpoint
pkg/contextmgr     分层上下文：system/mode/skills/project/recent → token 预算内动态分配
pkg/llm            引擎路由/重试 + OpenAI/Anthropic 适配器 + 模型目录
pkg/tools          工具注册表 + 内置工具 + 延迟加载 + HTTP/MCP 工具
pkg/agent/hooks    敏感文件检测 + bash 风险分级
pkg/permissions    四级权限决策链 + glob 匹配 + 规则持久化
pkg/session        JSONL 分支存储
pkg/checkpoint     轻量运行时快照
pkg/tui            Bubble Tea TUI，事件总线驱动
pkg/events         事件总线
pkg/observability  JSONL/Prometheus/HTML/SQLite/Markdown 导出
pkg/config         koanf 配置加载，环境变量覆盖
```

## SWE-bench Lite 评测

`eval/swe-bench-lite/` 提供 Docker 容器内评测框架，测量初次与反馈后修复率，生成含 token、缓存命中、工具链、模块耗时等指标的 HTML/Markdown 报告。

## 许可证

MIT
