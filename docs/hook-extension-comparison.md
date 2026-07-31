# Hook 与 Extension 机制对比

> 状态: ★★★/★★ 项已实施(2026-07-31) | 参考: Kimi Code v2 `externalHooks`、opencode `packages/plugin`、pi `core/extensions`
>
> 实施注记:shell `on_stop` 已包成 `ContinuationDecider`(exit 2 续跑 + stderr 经
> Steer 反馈);shell `before_compact` 已包装内建 summarizer;`tool_extensions`
> (JSON 描述符)已整体退役,外部工具一律走 MCP;extension 新增 `stop` /
> `session_start` / `permission` 三个 hook;权限插槽以 `Config.ExtraGuardPolicies`
> 落地(executor 装在 guard policies 末尾)。

## 1. Lcoder 现状盘点

Lcoder 实际有**四套**扩展/钩子机制,存在明显重叠:

| 机制 | 位置 | 能力 | 状态 |
|------|------|------|------|
| 进程内 Go hooks | `agent.Config`(BeforeToolCall/AfterToolCall/TransformContext/ShouldStop/ContinuationDeciders/ReminderProducers) | 编译期装配,全能力 | 活跃 |
| Shell config hooks | `config.Hooks` → `pkg/agent/hooks/shell.go` | before_tool_call、after_tool_result(进程外,exit code 语义) | **半活跃**:`before_compact`/`on_stop` 已声明于 `HookConfig` 但**从未接线**(死配置) |
| JSON-RPC extension runtime | `pkg/extension/`(~2190 行) | hooks(tool_call/tool_result/before_compact/input)、TUI 斜杠命令、事件订阅、session 自定义条目、git/local 安装、项目信任提示 | 活跃 |
| 外部工具 | `pkg/tools/extensions.go`(JSON 描述符 → HTTPExecutable)+ MCP | 给模型加工具 | 活跃但重叠 |

## 2. Hook 事件覆盖对比

| Hook 点 | Lcoder | Kimi v2 | opencode | pi |
|---------|:---:|:---:|:---:|:---:|
| PreToolUse / before tool | ✅(三种机制都有) | ✅ | ✅ `tool.execute.before` | ✅ `tool_call`(可改写) |
| PostToolUse / after tool | ✅ | ✅(含 Failure 区分) | ✅ `tool.execute.after` | ✅ `tool_result` |
| UserPromptSubmit / input | ✅(仅 extension runtime) | ✅ | ✅ `chat.message` | ✅ `input` |
| Stop(模型要停时) | ❌(`OnStop` 是死配置) | ✅ Stop/StopFailure | ❌ | ❌(用 agent_end 事件) |
| SessionStart | ❌ | ✅ | ❌ | ✅ `session_start` |
| PreCompact / 压缩介入 | ✅(仅 extension runtime 的 summarizer 替换) | ✅ PreCompact | ✅ `session.compacting` | ✅ `session_compact` |
| Permission 介入 | ❌(权限引擎不可 hook) | ❌(走自己的 permissionPolicy) | ✅ `permission.ask` | ❌ |
| LLM 请求改写(params/headers) | ⚠️ TransformContext 只改 messages | ❌ | ✅ `chat.params`/`chat.headers` | ⚠️ context 事件 |
| Notification | ❌ | ✅ | ❌ | ❌ |
| 总线事件订阅 | ✅(仅 extension runtime) | ✅(插件) | ✅ `event` catch-all | ✅ ~30 种细粒度事件 |
| 注册自定义工具 | ❌(扩展不能注册 Go 工具,只能 HTTP/MCP) | ✅ | ✅ `tool` | ✅ `registerTool` |
| 注册自定义命令 | ✅(extension runtime → TUI) | ✅ | ✅ | ✅ `registerCommand` + 快捷键 + CLI flag |
| 状态ful 扩展进程 | ✅(常驻 JSON-RPC 进程) | ✅ | ✅(进程内 JS) | ✅(进程内 JS) |

## 3. 主要差距

### 3.1 死配置:shell 的 before_compact / on_stop

`config.Hooks` 声明了五个 shell hook,`from_config.go` 只接线了两个。`before_compact`/`on_stop` 没有任何消费者——用户配置了也不会生效,属于误导性配置。注意:**goal 模式落地后,`on_stop` 有了天然接法——包成一个 `ContinuationDecider` 注册进链即可**(exit 2 = 不续跑),这正是 Kimi 的 Stop hook 语义。

### 3.2 权限决策不可 hook

opencode 的 `permission.ask` 让插件参与权限判定(如自动放行只读命令、按组织策略收紧)。Lcoder 的权限引擎(rules + guard policies + Ask 确认)完全封闭,扩展无法介入。这是合规/企业场景的关键 hook 点。

### 3.3 扩展不能注册一等工具

pi 的 `registerTool` 和 opencode 的 `tool` 都允许扩展注册带完整 schema 的工具(进程内执行)。Lcoder 的扩展要提供工具只能走 HTTP 描述符或 MCP——能用,但对"就在本机执行"的小工具来说偏重(MCP server 进程 vs 直接在扩展进程里处理)。

### 3.4 LLM 请求层不可见

opencode 的 `chat.params`/`chat.headers` 支持改写 temperature、注入 header(企业网关场景常见)。Lcoder 的 TransformContext 只覆盖 messages。

### 3.5 Kimi 独有的:SessionStart / Notification / PostToolUseFailure

SessionStart(恢复会话时注入上下文——常见于注入项目规约)、Notification(桌面通知)、PostToolUse 的 Failure 区分。价值中等,后补成本低。

## 4. Extension 系统的存在价值评估

### 4.1 JSON 描述符 HTTP 工具(`pkg/tools/extensions.go` + `config.ToolExtensionConfig`)

**结论:被 MCP 完全覆盖,建议退役。**

它做的事(声明一个 HTTP endpoint 当工具)MCP 全能做,而且 MCP 是行业标准协议,有现成生态(server 市场、SDK、调试工具)。保留它只是多一条需要维护的窄路。退役成本:删除 `pkg/tools/extensions.go`、`config.ToolExtensionConfig`、`LoadExtensions` 及配置样例,~300 行。

### 4.2 JSON-RPC extension runtime(`pkg/extension/`)

**结论:有存在价值,但定位需要收敛。**

它有三块别的机制给不了的能力:

1. **常驻有状态进程**。shell hook 是每事件起一次进程(无状态、慢);extension 是长驻进程,可以持有连接、缓存、计数器。Kimi/opencode/pi 的插件同样是有状态的(JS 进程内),Lcoder 是 Go 二进制,**进程外是引入任意语言扩展的唯一现实路径**(同 MCP 的立论)。
2. **summarizer 替换**(session_before_compact)。压缩策略是 agent 的核心行为,可替换它是研究级能力,其它机制给不了。
3. **TUI 命令注册 + 事件订阅 + session 条目**。这是"扩展做产品功能"(而非"扩展改 agent 行为")的入口,pi 的成功案例(extensions 生态)证明这条路的扩展性。

但它的**hook 部分与 shell config hooks 重叠度极高**(tool_call/tool_result 两处完全一样)。建议定位收敛为:

- **shell config hooks** = 零安装的轻量路径,覆盖简单的 before/after tool 拦截(保留,并把 on_stop/before_compact 接上或删掉)。
- **extension runtime** = 唯一的有状态、全能力扩展路径(hooks + 命令 + 事件 + summarizer),是 Lcoder 的"pi extensions"对应物。

### 4.3 与 goal 模式的协同(新出现的价值点)

goal 模式落地后,`ContinuationDecider` 链成为 Stop hook 的标准插槽:extension runtime 现在可以以极低成本支持 Kimi 的 `Stop` hook(goal 预算 veto 已经示范了链首 veto 的写法)。这是 extension 系统价值的增量而非存量。

## 5. 建议(按优先级)

| 优先级 | 行动 | 状态 |
|:---:|------|------|
| ★★★ | 接线或删除 shell 死配置:`on_stop` 包成 ContinuationDecider,`before_compact` 接到 SummarizeFunc | ✅ 已实施(2026-07-31) |
| ★★★ | 退役 JSON 描述符 HTTP 工具,文档引导到 MCP | ✅ 已实施(2026-07-31) |
| ★★ | extension runtime 增加 `stop` hook(经 decider 链)+ `session_start` | ✅ 已实施(2026-07-31) |
| ★★ | 权限引擎增加 hook 插槽(guard policy 形式已现成,把 extension 包成一个 policy) | ✅ 已实施(2026-07-31,`ExtraGuardPolicies`) |
| ★ | extension 支持注册一等工具(host 侧代理回扩展进程执行) | 未做 |
| ★ | SessionStart / Notification / PostToolUseFailure 区分 | SessionStart ✅;其余未做 |
| — | LLM params/headers 改写 | 等企业网关需求出现再做 |
