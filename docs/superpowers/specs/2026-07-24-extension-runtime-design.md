# Lcoder 代码级扩展体系设计

日期:2026-07-24
状态：已确认(4 节设计逐节经用户批准)

## 背景与目标

Lcoder 的声明式扩展面(MCP / HTTP tools / skills / modes)与 reference/pi 同代，但代码级扩展面存在明显差距：唯一载体是 Go plugin(`-buildmode=plugin`，不支持 Windows、要求 Go/依赖版本一致、无热重载)，且只有 4 个进程内钩子。pi 的核心优势是"事件驱动 + 全表面 ExtensionAPI"。

本设计用**进程外 JSON-RPC 扩展运行时**替换 Go plugin，覆盖四件事:

1. 开放事件总线订阅 + 可干预钩子
2. 进程外扩展运行时(stdio JSON-RPC 2.0 自定义协议)
3. Session custom entry(扩展状态持久化)
4. 扩展命令注册(TUI slash 命令)

非目标：扩展注册自定义 LLM 工具(pi 的 registerTool)、UI 表面(dialogs/widgets)、`custom_message` 注入上下文变体、扩展命令覆盖内置命令。均为 YAGNI，留待有需求时扩展。

## 已确认的边界条件

- 可干预钩子:`tool_call` 拦截、`tool_result` 修改、`session_before_compact` 压缩自定义、`input` 拦截
- 加载位置：全局 `~/.lcoder/extensions/` + 项目级 `.lcoder/extensions/`，项目级需用户确认(信任门)
- 进程外方案已接受代价：钩子 IPC 往返毫秒级延迟

## §1 进程模型与协议

### 扩展 = 外部进程

每个扩展一个目录，内含 `extension.yaml` 清单(只管启动):

```yaml
name: my-ext
version: 0.1.0
command: ["node", "index.js"]   # 或 ["python", "ext.py"]、["./ext-bin"]
env: { KEY: "value" }
```

发现位置:`~/.lcoder/extensions/<name>/`(全局)+ `.lcoder/extensions/<name>/`(项目级，信任门见 §4)。

### 传输

stdio 上换行分隔的 JSON-RPC 2.0(request/response/notification，与 MCP stdio 相同 framing)。进程生命周期管理借鉴 `pkg/mcp`:spawn、stderr 捕获、死亡检测、退出时 kill + 宽限期。

### 握手

host spawn 后发送 `initialize` → 扩展返回能力声明:

```json
{ "name": "my-ext", "version": "0.1.0",
  "events": ["turn_start", "tool_execution_end"],
  "hooks": ["tool_call", "tool_result"],
  "commands": [{"name": "review", "description": "..."}] }
```

### 消息面(host → ext)

| 类型 | 方法 | 语义 |
|---|---|---|
| notification | `event/<name>` | 事件订阅，单向，payload 复用 `pkg/events` 类型的 JSON |
| request | `hook/tool_call` | `{tool, params}` → `{action: allow\|block, reason?, params?}`(可改参数) |
| request | `hook/tool_result` | `{tool, params, result, is_error}` → `{result?}`(可改写结果) |
| request | `hook/session_before_compact` | `{conversation, tokens_before}` → `{summary}`(自定义压缩摘要) |
| request | `hook/input` | `{text}` → `{action: continue\|transform\|block, text?}` |
| request | `command/invoke` | `{name, args}` → 扩展执行已注册命令 |

ext → host 反向最小集：`session/append_entry`(custom entry，见 §3)、`session/get_entries`(读取本扩展 entry)、`host/log`(经事件总线显示)。

### 多扩展链式

按加载顺序依次调用。`tool_call` 任一返回 block 即阻断(fail-safe，对齐 pi)；参数修改链式传递。`tool_result` 改写链式传递。

### 错误策略

- hook 超时(默认 5s，可配)→ 放行 + 发 warning 事件(fail-open)
- 扩展返回 block → 阻断
- 进程崩溃 → 标记 dead、跳过其所有 hook/事件、warning 一次
- 压缩钩子超时/失败 → 回退内建 summarizer
- host 取消(如用户中断)→ 发 JSON-RPC cancel 通知

## §2 桥接：钩子接入点与 Go plugin 退役

核心原则：**bridge 只做适配器，不改 agent loop 的控制流**。现有钩子接口已够用，桥接层把进程外调用包装成这些接口的实现。

| 扩展钩子 | 接入点 | 说明 |
|---|---|---|
| `hook/tool_call` | `executor` 的 `BeforeToolCallHook` | 聚合所有声明该钩子的扩展按序调用;block → 返回权限拒绝式错误；参数改写 → 替换 tool call 的 arguments |
| `hook/tool_result` | `AfterToolCallHook` | 链式改写 result 文本后写回消息 |
| `hook/session_before_compact` | `contextmgr.SummarizeFunc` | 有扩展声明则调用之(`SerializeConversation` 序列化后传入),无声明或失败回退内建 LLM summarizer;`context.Context` 取消映射为协议 cancel，保持可中止语义 |
| `hook/input` | TUI `runner` 提交路径 + one-shot 模式 | 在 skill 手动触发解析**之前**拦截;transform → 替换输入文本,block → 不提交并显示 reason |

事件订阅：bridge 在 `events.Bus` 上注册一个 `Subscribe` 回调，按扩展声明的事件名过滤后转发为 `event/<name>` 通知。payload 直接复用 `pkg/events` 各事件类型已有的 JSON 序列化，零新类型。

### Go plugin 退役

删除 `pkg/extension/plugin.go`(`PluginLoader`,`.so` 加载)。项目处于开发阶段、无用户，不做兼容层。`Loader`/`Manager`(包安装、skill/agent 目录管理)保留不动。`Extension` 接口(`RegisterTools`/`RegisterHooks`/`RegisterExporters`)重新定义为进程内宿主接口——bridge 是其一个实现；配置内建 Hook(Audit/SensitiveFileCheck/BashDenylist)继续走进程内路径不受影响。

### 接线位置

`cmd/lcoder/main.go` 的 `prepareAgent` 中，agent 构建完成后启动扩展 host(发现 → 信任门 → spawn → 握手 → bridge 注册钩子/事件/命令)。TUI 退出或 one-shot 结束时统一 shutdown 所有扩展进程。

失败兜底：扩展 host 整体启动失败(如清单解析错误)不阻塞主程序——warning 后以无扩展模式运行，与 MCP server 启动失败的既有策略一致。

## §3 Session Custom Entry

目标：扩展能把状态/展示信息持久化进 session JSONL，对齐 pi 的 `appendEntry`，且不污染模型上下文。

### 存储形态

复用现有 append-only JSONL 和 compaction entry 的先例——custom entry 是 `Metadata` 带标记的普通记录，不新增文件格式:

```json
{ "id": "...", "parent_id": "...", "role": "custom",
  "metadata": { "type": "custom", "custom_type": "my-ext/state",
                "branch_id": "main", "data": { ...任意 JSON... } } }
```

- `role: "custom"` 新枚举值，与 `user/assistant/tool` 区分;`custom_type` 命名空间约定 `<ext-name>/<key>`，扩展只能读自己的数据
- 不进上下文:`EffectiveMessages()` 与 `ActiveMessages()` 跳过 `role=custom` 记录(compaction 序列化、token 估算同样忽略)
- 分支语义免费获得:entry 带 `parent_id` + `branch_id`，挂在当前 leaf 上,fork/切换分支后扩展读到的就是自己分支的历史

### API(`pkg/session` 新增)

```go
func (s *Session) AppendCustomEntry(customType string, data json.RawMessage) error
func (s *Session) CustomEntries(customType string) []CustomEntry  // 按 active 分支过滤
```

协议映射：扩展发 `session/append_entry {custom_type, data}` → bridge 校验 `custom_type` 前缀属于该扩展 → 追加。读取经 `session/get_entries` 返回该扩展名下的全部 entry(重连恢复状态用)。

### 测试要点

追加 → 重载后 entry 存活且不出现在 `EffectiveMessages()`;fork 后两分支各自只见自己的 entry;compaction entry 与 custom entry 交错时 `EffectiveMessages()` 行为不变。

## §4 命令注册、信任门与配置

### 命令注册

- `pkg/tui` 的 slash 命令从硬编码 switch 抽象为 `CommandRegistry`:`Register(name, description, usage, handler)` + 补全列表来源；内置命令迁移为首批注册项，行为不变
- 扩展握手声明 `commands`;bridge 注册 handler:invoke → 向扩展发 `command/invoke {name, args}` → 扩展执行 → 经 `host/log` 或 invoke 响应返回文本 → TUI 显示为 system 行
- 名称冲突：与内置/其他扩展重名时拒绝注册并 warning(不做覆盖语义)
- one-shot 模式不注册扩展命令(无交互界面),事件与钩子照常生效

### 信任门

- 全局扩展默认可信，直接加载(与 `~/.lcoder/config.yaml` 同等信任级别)
- 项目级扩展每次启动、逐个向用户确认后才 spawn——复用权限引擎的 `Ask` 通道(TUI 弹确认框;one-shot/JSON 模式无法交互则跳过并 warning，可用 `--trust-project-extensions` 旗标预授权)
- 拒绝的扩展本次会话不再询问，记 warning

### 配置(`lcoder.yaml` 新增 `extensions:` 节)

```yaml
extensions:
  disabled: ["my-ext"]        # 按名禁用
  hook_timeout_ms: 5000       # 全局 hook 超时
```

### 测试要点

握手声明命令 → TUI 补全可见、invoke 转发正确；重名拒绝；项目级未经确认不 spawn、确认后加载；禁用列表生效；扩展进程崩溃后命令调用返回错误而不 panic。

## 包结构(新增/变更)

- `pkg/extension/proto` — JSON-RPC 2.0 framing、方法名与 payload 类型
- `pkg/extension/runtime` — 进程 spawn/生命周期、连接管理、请求分发、超时/取消
- `pkg/extension/bridge` — 钩子/事件/命令适配进 agent、contextmgr、TUI、session
- `pkg/extension/plugin.go` — **删除**(Go plugin 退役)
- `pkg/session` — `AppendCustomEntry` / `CustomEntries` / `role=custom` 过滤
- `pkg/tui` — `CommandRegistry` 抽象，内置命令迁移
- `pkg/config` — `extensions:` 配置节
- `cmd/lcoder/main.go` — 扩展 host 接线与 shutdown

## 测试策略

- 单元测试用进程内 fake 扩展(直接对连 `runtime.Connection` 两端，不起真实进程)覆盖协议与桥接
- 少数端到端测试用 Go 写的 helper 扩展二进制(`go build` 到临时目录)验证 spawn/握手/崩溃隔离
- 沿用既有约定:`go test $(go list ./... | grep -v 'reference/Shannon')`,集成测试用 `integration` build tag
