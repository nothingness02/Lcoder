# Lcoder 扩展与配置手册

Lcoder 提供多种扩展机制，按实现复杂度从低到高排列。

## 扩展方式对比

| 方式 | 类型 | 复杂度 | 能力 |
|------|------|:---:|------|
| [技能 (Skills)](#1-技能-skills) | Markdown 文件 | ★ | 注入领域知识 |
| [自定义模式 (Modes)](#2-自定义模式-modes) | YAML 配置 | ★ | 限定工具集 + system prompt |
| [HTTP 工具 (HTTP Tools)](#3-http-工具) | YAML 配置 | ★★ | REST 端点作为工具 |
| [MCP 服务器 (MCP Servers)](#4-mcp-服务器) | 外部进程 | ★★ | 任意自定义工具 |
| [扩展 (Extensions)](#5-扩展-extensions) | JSON-RPC 子进程 | ★★★ | Hooks / 事件 / 命令 / 会话 |

---

## 1. 技能 (Skills)

在 `.lcoder/skills/<name>/SKILL.md` 中编写 Markdown，注入领域知识到 system prompt。

### 示例

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
- 错误响应格式：`{ "error": { "code": "...", "message": "..." } }`
- 所有接口需要认证头 `Authorization: Bearer <token>`
```

### 配置项

| 字段 | 说明 |
|------|------|
| `description` | 技能描述，触发匹配时显示 |
| `allowed_tools` | 激活后限制 agent 可用的工具列表 |

```bash
# 列出已发现的技能
./lcoder skills
```

---

## 2. 自定义模式 (Modes)

在 `.lcoder/modes/<name>.yaml` 或 `~/.lcoder/modes/<name>.yaml` 中定义 mode。

### 示例

参阅 [custom-mode/security-review.yaml](custom-mode/security-review.yaml)：

```yaml
name: security-review
description: 专注安全审查，仅允许只读工具
prompt: |
  你是一个安全审查专家...
allowed_tools:
  - read
  - ls
  - grep
  - find
require_approval_to_exit: true
```

### 配置项

| 字段 | 说明 |
|------|------|
| `name` | 模式名（必填） |
| `description` | 描述 |
| `prompt` | 附加 system prompt |
| `allowed_tools` | 白名单工具列表 |
| `tools.deny` | 黑名单规则（`{tool: {pattern: deny}}`） |
| `tools.ask` | 需审批的规则（`{tool: {pattern: ask}}`） |
| `tools.allow` | 自动允许的规则 |
| `model` | 指定模型（空=继承） |
| `provider` | 指定 provider（空=继承） |
| `require_approval_to_exit` | 退出模式需审批 |

```bash
# 列出可用模式
./lcoder modes

# 指定模式运行
./lcoder --mode security-review

# TUI 中切换
/modes
```

---

## 3. HTTP 工具

在 `~/.lcoder/config.yaml` 中声明 REST 端点作为 agent 工具。

### 示例

```yaml
http_tools:
  - name: deploy
    endpoint: http://localhost:9001/deploy
    description: 部署到预发布环境
    execution_mode: sequential
    parameters:
      type: object
      properties:
        service: { type: string, description: 服务名 }
        version: { type: string, description: 版本号 }
      required: [service]
    headers:
      Authorization: Bearer ${DEPLOY_TOKEN}
```

### 配置项

| 字段 | 说明 |
|------|------|
| `name` | 工具名（必填） |
| `endpoint` | HTTP POST 地址（必填） |
| `description` | 工具描述 |
| `parameters` | JSON Schema 参数定义 |
| `execution_mode` | `parallel`（默认）或 `sequential` |
| `headers` | HTTP 请求头，支持 `${ENV}` 环境变量 |

---

## 4. MCP 服务器

通过 Model Context Protocol 接入外部工具服务器。

### 传输方式

| 传输 | 说明 | 示例 |
|------|------|------|
| `stdio` | 子进程标准输入输出 | `command: ["npx", "-y", "@modelcontextprotocol/server-filesystem", "."]` |
| `sse` | Server-Sent Events | `url: http://localhost:3000` |
| `streamable-http` | HTTP 流式传输 | `url: https://mcp.example.com/v1` |

### 示例

```yaml
mcp_servers:
  # 代码智能（推荐 codegraph）
  - name: codegraph
    transport: stdio
    command: ["codegraph", "serve", "--mcp", "--path", "."]

  # 远程服务
  - name: remote
    transport: sse
    url: http://localhost:3000
    headers:
      Authorization: Bearer ${REMOTE_TOKEN}
    timeout: 60
```

### 配置项

| 字段 | 说明 |
|------|------|
| `name` | 服务器名（必填），工具显示为 `{name}_{tool}` |
| `transport` | `stdio` / `sse` / `streamable-http`（必填） |
| `command` | stdio 模式的启动命令 |
| `env` | stdio 模式的环境变量 |
| `url` | sse / streamable-http 模式的地址 |
| `headers` | HTTP 请求头 |
| `timeout` | 超时秒数（默认 120） |

```bash
# TUI 中查看 MCP 状态
/mcp
```

---

## 5. 扩展 (Extensions)

**真正的可编程扩展**——独立子进程通过 JSON-RPC 2.0 与 Lcoder 通信。

### 架构

```
Lcoder Host                          Extension Process
    │                                      │
    │── initialize ──────────────────────→│  声明 hooks/events/commands
    │←─ { hooks: [...], events: [...] } ──│
    │                                      │
    │── hook/tool_call ──────────────────→│  每次工具调用前
    │←─ { action: "allow"|"block" } ──────│
    │                                      │
    │── hook/tool_result ────────────────→│  每次工具调用后
    │←─ { result: "rewritten" } ──────────│
    │                                      │
    │── hook/input ──────────────────────→│  用户输入时
    │←─ { action: "transform"|"block" } ──│
    │                                      │
    │── event/turn_start ────────────────→│  事件广播（通知，无回复）
    │                                      │
    │── shutdown ────────────────────────→│  关闭
```

### 目录结构

```
examples/sensitive-guard/
├── extension.yaml    # 扩展声明
├── go.mod
└── main.go           # 扩展逻辑
```

### extension.yaml

```yaml
name: sensitive-guard          # 扩展名
version: "0.1.0"
command: ["go", "run", "."]    # 启动命令
env:                           # 环境变量（可选）
  BLOCK_PATTERNS: ".env,id_rsa"
```

### 扩展代码骨架

```go
// 1. 从 stdin 逐行读取 JSON-RPC 请求
scanner := bufio.NewScanner(os.Stdin)
for scanner.Scan() {
    var req request
    json.Unmarshal(scanner.Bytes(), &req)

    // 2. 根据 method 分发
    switch req.Method {
    case "initialize":
        // 返回扩展能力声明
        result = initializeResult{
            Name:  "my-extension",
            Hooks: []string{"tool_call"},
        }
    case "hook/tool_call":
        // 处理钩子
        result = handleToolCall(req.Params)
    }

    // 3. 输出 JSON-RPC 响应
    resp := response{ID: *req.ID, Result: result}
    data, _ := json.Marshal(resp)
    fmt.Println(string(data))
}
```

### 支持的 Hook

| Hook | 协议方法 | 能力 |
|------|---------|------|
| `tool_call` | `hook/tool_call` | block 工具调用 / 修改参数 |
| `tool_result` | `hook/tool_result` | 改写工具返回文本 |
| `session_before_compact` | `hook/session_before_compact` | 生成上下文压缩摘要 |
| `input` | `hook/input` | block 或 transform 用户输入 |

### 支持的 Events（订阅通知）

`turn_start`, `turn_end`, `message_start`, `message_end`, `tool_execution_start`, `tool_execution_end`, `compaction_committed`

### 支持的 Commands

扩展可声明自定义 TUI 命令：

```go
result = initializeResult{
    Commands: []commandDecl{
        {Name: "review", Description: "审查当前文件", Usage: "/review"},
    },
}
```

### 宿主配置

```yaml
# ~/.lcoder/config.yaml
extensions:
  dirs:
    - ~/.lcoder/extensions
    - ./examples/sensitive-guard
```

### 完整示例

参阅 [sensitive-guard/main.go](sensitive-guard/main.go)——阻止对 `.env`、SSH 密钥等敏感文件的写操作。

```bash
# 编译验证
cd examples/sensitive-guard
go build ./...
```

### 协议细节

- 传输：newline-delimited JSON-RPC 2.0 over stdin/stdout
- 超时：默认 5s per hook call
- 错误处理：hook 错误 fail-open（放行），before_compact 错误 fail-closed（退回内置摘要器）
- 死亡检测：子进程退出后自动跳过其 hook

---

## 扩展点总结

```
                    ┌─ 技能 (Markdown)     → system prompt 注入
                    ├─ 模式 (YAML)          → system prompt + 工具约束
用户可扩展 ─────────┼─ HTTP 工具 (YAML)     → REST 端点 → agent 工具
                    ├─ MCP 服务器 (子进程)   → 任意自定义工具
                    └─ 扩展 (JSON-RPC)      → Hooks + Events + Commands
                              │
                    ┌────────┴────────┐
                    │ tool_call       │  工具调用前拦截/修改
                    │ tool_result     │  工具结果后改写
                    │ before_compact  │  上下文压缩摘要
                    │ input           │  用户输入拦截/变换
                    │ event/*         │  事件通知订阅
                    │ command/*       │  自定义斜杠命令
                    └─────────────────┘
```
