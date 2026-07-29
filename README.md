# Lcoder

[English Version](README_EN.md)

**Lcoder** 是一个纯 Go 实现的 AI 编程助手 —— 单二进制、零依赖、终端原生。

> 🎯 **推荐体验：交互式 TUI。** 输入 `./lcoder` 就能获得完整的编程对话体验 —— 实时流式输出、工具调用可视化、会话分支、权限审批，一切都在终端内完成。

## 快速开始

```bash
# 编译（仅需 Go 工具链，无 Node/Python 依赖）
go build -o lcoder ./cmd/lcoder

# 配置 API key
export ANTHROPIC_API_KEY="sk-ant-..."   # Anthropic（推荐）
export OPENAI_API_KEY="sk-..."          # OpenAI 兼容
export DEEPSEEK_API_KEY="sk-..."        # DeepSeek

# 启动交互式 TUI
./lcoder
```

首次运行会自动创建 `~/.lcoder/config.yaml`，之后按 `Enter` 即可开始对话。

### Docker 启动

```bash
# 克隆项目
git clone https://github.com/nothingness02/Lcoder.git
cd Lcoder/docker

# 创建工作目录（把你要 AI 辅助的项目放进去）
mkdir workspace

# 启动（首次会自动编译镜像，约 2 分钟）
ANTHROPIC_API_KEY="sk-ant-..." docker compose up
```

Docker 自动完成 Go 编译、容器化，挂载 `./workspace` 为工作目录，配置和会话通过命名卷持久化。

### 其他使用方式

```bash
./lcoder -p "列出当前目录的文件"          # 单次对话（适合脚本）
./lcoder -c                              # 继续最近一次会话
./lcoder --mode plan -p "设计认证模块"     # 指定模式
```

## TUI 交互指南

Lcoder 的终端界面围绕“对话即编程”设计。输入自然语言描述需求，agent 会自动读取代码、编辑文件、执行命令，你随时审批关键操作。

### 主界面

```
┌─ Lcoder · code · claude-sonnet-4-20250514 ── 会话: 3 ── 轮次: 12 ── 22K ─┐
│                                                                           │
│  You: 给 handler.go 加一个请求频率限制中间件                                  │
│                                                                           │
│  Lcoder: 我来实现。先看看现有的 handler 和项目结构。                           │
│  │   read handler.go                                          展开 ▼      │
│  │   ls pkg/middleware/                                                    │
│  │   write pkg/middleware/ratelimit.go    ✓ Wrote 1,247 bytes              │
│  │   已创建频率限制中间件，在 handler.go 中注册路由即可。                        │
│                                                                           │
│  ▸ 输入消息...                                          Ctrl+H 帮助        │
└───────────────────────────────────────────────────────────────────────────┘
```

### 快捷键

| 快捷键 | 功能 |
|--------|------|
| `Enter` | 发送消息 |
| `Shift+Enter` | 换行 |
| `Ctrl+O` | 展开/折叠工具调用详情 |
| `Ctrl+T` | 任务进度面板（todo_write 可视化） |
| `Ctrl+B` | 从最后一条回复分叉新会话 |
| `Ctrl+R` | 重试最后一条回复 |
| `Ctrl+S` | 浏览和切换历史会话 |
| `Ctrl+M` | MCP / HTTP 扩展工具管理 |
| `Ctrl+L` | 清空当前会话 |
| `PgUp` / `PgDn` | 滚动对话历史 |

### 斜杠命令

在输入框输入以下命令快速操作：

| 命令 | 功能 |
|------|------|
| `/modes` | 切换 agent 模式（code / plan / explore / review） |
| `/mcp` | 查看和重连 MCP 服务器 |
| `/tasks` | 打开/关闭任务侧边栏 |
| `/tools` | 展开/折叠所有工具结果 |
| `/help` | 显示帮助信息 |

## 为什么选择 Lcoder

| 特性 | Lcoder | 其他 agent |
|------|:---:|:---:|
| **单二进制** | ✅ `go build` 即用 | 多数需要 Node.js |
| **路径安全** | ✅ 敏感文件 + workspace 边界守卫 | 多数无 |
| **多模式** | ✅ code / plan / explore / review | 多数单一模式 |
| **会话分支** | ✅ 任意消息分叉、克隆 | 少数支持 |
| **崩溃恢复** | ✅ 检查点自动保存和还原 | 多数不支持 |
| **上下文压缩** | ✅ 分层压缩 + cache 策略 | 多数简单截断 |
| **权限引擎** | ✅ 四级决策链 + 审计日志 | 参差不齐 |
| **可观测性** | ✅ Prometheus / HTML / SQLite | 多数无内建 |
| **子 Agent** | ✅ 并行 + swarm 集群模式 | 少数支持 |
| **扩展系统** | ✅ MCP + HTTP 工具 + 扩展桥 | MCP 支持不一 |

## 核心能力

### 🧠 多模式 Agent

四种内建模式在 TUI 中一键切换（`/modes`），各有独立 prompt 和工具约束：

| 模式 | 用途 | 可用工具 |
|------|------|:---:|
| `code` | 日常编码实现 | 全部 |
| `plan` | 需求分析和方案设计 | 只读 + todo |
| `explore` | 探索和理解代码库 | 只读 |
| `review` | 代码审查 | 只读 |

支持自定义模式（`.lcoder/modes/*.yaml`），可指定独立 provider/model、工具规则、退出审批。

### 🛡️ 路径安全守卫

对标 Kimi Code 的安全架构。所有文件操作在执行前统一经过安全校验：

- `.env`、SSH 私钥、credentials 永久不可读写
- `../` 路径逃逸自动拒绝，外部文件需绝对路径
- 守卫在权限引擎之前运行，敏感操作不会弹出审批框

### 📦 技能系统

在 `.lcoder/skills/` 下放 Markdown 文件即可添加领域知识：

```
.lcoder/skills/
├── security-review/SKILL.md   # 安全审查流程
├── api-design/SKILL.md        # API 设计规范
└── db-migration/SKILL.md      # 数据库迁移指南
```

TUI 中输入相关内容时会自动提示匹配的技能。

### 🔀 会话管理

每条消息记录 `parent_id`，支持完整的分支操作：

- `Ctrl+B` 从最后回复分叉新会话
- `Ctrl+S` 浏览和恢复历史会话
- `Ctrl+R` 用不同思路重试同一条回复
- `-c` 参数继续最近会话

### 🔐 权限引擎

write / edit / bash 默认需审批，TUI 中弹框确认：

```
┌─ 审批 ──────────────────────────────────────────────┐
│  bash: go test ./...                                 │
│                                                      │
│  [y] 允许本次  [a] 项目记住  [g] 全局记住  [n] 拒绝   │
└──────────────────────────────────────────────────────┘
```

支持 once / project / global 三级审批作用域，完整审计日志。

### 🔌 扩展系统

在 `~/.lcoder/config.yaml` 中注册即可扩展 agent 能力：

```yaml
# MCP 服务器（推荐用 codegraph 做代码智能）
mcp_servers:
  - name: codegraph
    transport: stdio
    command: ["codegraph", "serve", "--mcp", "--path", "."]

# HTTP 工具
http_tools:
  - name: deploy
    endpoint: http://localhost:9001/deploy
    parameters:
      type: object
      properties:
        service: { type: string }
```

### 🐝 子 Agent 集群

在对话中直接描述并行任务，agent 自动拆分执行：

> 同时做三件事：修复 handler.go 的 bug、给 service.go 加测试、更新 README

多个子 agent 并发运行，结果自动汇总。TUI 中实时显示每个子 agent 的进度。

### 📊 上下文管理

自动分层组织对话上下文，智能压缩不丢失关键信息：

- 稳定层（system prompt / 项目文档 / 技能）始终保留
- 滑动窗口（最近消息）按 token 预算动态调整
- 支持 Anthropic prompt cache，减少重复计费

### 📈 可观测性

```bash
./lcoder stats <id>                    # 会话统计
./lcoder export <id>                   # HTML 报告
./lcoder export --format sqlite -o.db  # SQLite 数据库
./lcoder metrics                       # Prometheus (:9090)
```

## 架构

```
Lcoder 单二进制
├── TUI (Bubble Tea)        终端交互界面
├── Agent                    多轮对话编排
│   ├── 权限引擎             四级决策链
│   ├── 上下文管理           分层 token 预算
│   └── 检查点               崩溃自动恢复
├── LLM 引擎                 OpenAI / Anthropic / DeepSeek
├── 工具系统
│   ├── 内置工具             read write edit bash ls grep find
│   ├── MCP 客户端           stdio / SSE / Streamable HTTP
│   ├── HTTP 工具            自定义 REST 端点
│   └── 扩展桥              子进程通信
└── 可观测性                 JSONL / Prometheus / HTML / SQLite
```

## 许可证

MIT
