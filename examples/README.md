# Lcoder 扩展与配置手册

## 快速选择

| 你想做什么 | 用什么 |
|-----------|--------|
| 阻止 agent 改某些文件 | shell hook: `before_tool_call` |
| 记录每次工具调用 | shell hook: `after_tool_result` |
| 在 agent 说完后自动继续 | shell hook: `on_stop` |
| 注入领域知识（代码规范、设计模式） | 技能 (Skill) |
| 限制 agent 只能用只读工具 | 模式 (Mode) |
| 接入外部 API 作为工具 | HTTP 工具 或 MCP |
| 接入代码智能（codegraph） | MCP |
| 写一个长期运行的自定义服务 | MCP 或 扩展 |

---

## 扩展方式概览

| 方式 | 配置 | 复杂度 | 场景 |
|------|------|:---:|------|
| [Shell Hook](#1-shell-hook) | YAML 命令 | ★ | 工具调用前后执行脚本 |
| [技能 (Skills)](#2-技能-skills) | Markdown | ★ | 注入领域知识 |
| [模式 (Modes)](#3-自定模式-modes) | YAML | ★ | 限定工具集 + prompt |
| [HTTP 工具](#4-http-工具) | YAML | ★★ | REST 端点作为工具 |
| [MCP 服务器](#5-mcp-服务器) | 外部进程 | ★★ | 任意自定义工具 |
| [扩展 (Extensions)](#6-扩展-extensions) | JSON-RPC | ★★★ | 高级：状态保持、事件 |

---

## 1. Shell Hook

**统一的钩子机制。** 对标 Kimi Code，所有 hook 都是 shell 命令。

### 协议

```
Lcoder spawn sh -c "your-command"
  │
  ├── stdin  ← JSON 上下文（hook_event, tool_name, tool_input, session_id...）
  │
  └── 等待退出
       ├── exit 0  → allow（允许操作）
       ├── exit 2  → block（stderr 成为拒绝原因，反馈给模型）
       └── 其他     → allow（fail-open：hook 崩溃不阻塞 agent）
```

### 可用事件

| Hook | 触发时机 | 能做什么 |
|------|---------|---------|
| `before_tool_call` | 每次工具调用前 | block 操作 / 修改参数 |
| `after_tool_result` | 每次工具调用后 | 记录日志 / 改写输出 |
| `before_compact` | 上下文压缩前 | 生成自定义摘要 |
| `on_stop` | agent 停止时 | 注入后续任务 |

### 配置

```yaml
# ~/.lcoder/config.yaml
hooks:
  before_tool_call:
    enabled: true
    command: "python3 ~/.lcoder/hooks/guard.py"
    timeout: 30

  after_tool_result:
    enabled: true
    command: "python3 ~/.lcoder/hooks/log.py"
```

### 示例：阻止修改敏感文件

参阅 [hooks/guard-sensitive-files.py](hooks/guard-sensitive-files.py)。

```yaml
hooks:
  before_tool_call:
    enabled: true
    command: "python3 ./examples/hooks/guard-sensitive-files.py"
```

### 示例：记录所有 bash 命令

参阅 [hooks/log-bash.py](hooks/log-bash.py)。

```yaml
hooks:
  after_tool_result:
    enabled: true
    command: "python3 ./examples/hooks/log-bash.py"
```

### 安全特性

- **超时强制终止**：默认 30s，到期杀整个进程树
- **fail-open**：超时/崩溃 = 放行，不阻塞 agent
- **进程树清理**：POSIX `kill(-pid)` / Windows `taskkill /T`

---

## 2. 技能 (Skills)

在 `.lcoder/skills/<name>/SKILL.md` 中编写 Markdown，注入领域知识到 system prompt。

```
.lcoder/skills/
└── api-design/
    └── SKILL.md
```

```markdown
---
description: RESTful API 设计规范
allowed_tools: [read, ls, grep, find]
---

## API 设计规范

- 使用 RESTful 风格，资源名用复数名词
- 分页参数统一为 `page` / `page_size`
- 错误响应：`{ "error": { "code": "...", "message": "..." } }`
```

| 字段 | 说明 |
|------|------|
| `description` | 技能描述，触发匹配时显示 |
| `allowed_tools` | 激活后限制 agent 可用的工具 |

```bash
./lcoder skills    # 列出已发现的技能
```

---

## 3. 自定模式 (Modes)

在 `.lcoder/modes/<name>.yaml` 中定义 agent 模式。

参阅 [custom-mode/security-review.yaml](custom-mode/security-review.yaml)：

```yaml
name: security-review
description: 安全审查
prompt: "你是安全专家，检查代码漏洞..."
allowed_tools: [read, ls, grep, find]
require_approval_to_exit: true
```

| 字段 | 说明 |
|------|------|
| `name` | 模式名 |
| `prompt` | 附加 system prompt |
| `allowed_tools` | 白名单 |
| `tools.deny` / `tools.ask` / `tools.allow` | 权限规则 |
| `model` / `provider` | 指定模型（空=继承） |
| `require_approval_to_exit` | 退出需审批 |

```bash
./lcoder --mode security-review   # 命令行指定
/modes                             # TUI 中切换
```

---

## 4. HTTP 工具

在 `~/.lcoder/config.yaml` 中声明 REST 端点作为 agent 工具。

```yaml
http_tools:
  - name: deploy
    endpoint: http://localhost:9001/deploy
    description: 部署到预发布
    parameters:
      type: object
      properties:
        service: { type: string }
      required: [service]
    headers:
      Authorization: Bearer ${DEPLOY_TOKEN}
```

| 字段 | 说明 |
|------|------|
| `name` | 工具名 |
| `endpoint` | HTTP POST 地址 |
| `parameters` | JSON Schema |
| `execution_mode` | `parallel` / `sequential` |
| `headers` | 请求头，支持 `${ENV}` |

---

## 5. MCP 服务器

通过 Model Context Protocol 接入外部工具。

```yaml
mcp_servers:
  - name: codegraph
    transport: stdio
    command: ["codegraph", "serve", "--mcp", "--path", "."]

  - name: remote-api
    transport: sse
    url: http://localhost:3000
    timeout: 60
```

| 传输 | 场景 |
|------|------|
| `stdio` | 本地子进程 |
| `sse` | HTTP 长连接 |
| `streamable-http` | HTTP 流式 |

```bash
/mcp    # TUI 中查看 MCP 状态
```

---

## 6. 扩展 (Extensions)

> Shell hook 覆盖 90% 场景。仅在需要进程常驻、事件订阅、会话持久化时使用扩展。

独立子进程，通过 JSON-RPC 2.0 与 Lcoder 通信。参阅 [sensitive-guard/main.go](sensitive-guard/main.go)。

```yaml
# extension.yaml
name: my-extension
command: ["go", "run", "."]
```

```yaml
# ~/.lcoder/config.yaml
extensions:
  dirs: ["~/.lcoder/extensions"]
```

### 扩展独有能力

| 能力 | 说明 |
|------|------|
| 事件订阅 | 接收 `event/turn_start` 等实时通知 |
| 会话数据 | `session/append_entry` 读写自定义条目 |
| 自定义命令 | 声明 TUI 斜杠命令 |
| 进程常驻 | 保持状态，避免每次冷启动 |
